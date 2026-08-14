package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/ylxmf2005/omnihub/internal/core"
	xproxy "golang.org/x/net/proxy"
)

var (
	ErrInvalidProfile      = errors.New("invalid egress profile")
	ErrInvalidCredential   = errors.New("invalid egress credential")
	ErrUntrustedTransport  = errors.New("untrusted transport")
	ErrCleartextCredential = errors.New("credentialed request requires a protected target path")
)

// Client 是 Build 唯一能构造出有效状态的可信 HTTP client。transport 不导出，
// 因此 Adapter 可以包装它做请求签名，却不能把调用方注入的 RoundTripper 冒充成它。
type Client struct {
	transport *trustedTransport
}

type trustedTransport struct {
	base    *http.Transport
	profile core.EgressProfile
	probe   *Probe
	proxied atomic.Bool
	proxy   func(*http.Request) (*url.URL, error)
}

// Build 只从已经解析的 EgressProfile 与其 Credential 构造具体 transport。
// 不接受外部 RoundTripper，也不做任何出口 fallback。
func Build(profile core.EgressProfile, credential *core.Credential, probe *Probe) (*Client, error) {
	if !profile.Enabled || profile.Validate() != nil {
		return nil, ErrInvalidProfile
	}
	username, password, err := proxyBasicCredential(profile, credential)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	trusted := &trustedTransport{base: transport, profile: profile, probe: probe}
	switch profile.Mode {
	case core.EgressModeDirect:
		// Proxy=nil is an explicit direct route.
	case core.EgressModeEnvironment:
		trusted.proxy = http.ProxyFromEnvironment
		transport.Proxy = trusted.proxyForRequest
	case core.EgressModeHTTPProxy:
		proxyURL, _ := url.Parse(profile.ProxyEndpoint)
		if username != "" {
			privateURL := *proxyURL
			privateURL.User = url.UserPassword(username, password)
			proxyURL = &privateURL
		}
		trusted.proxy = http.ProxyURL(proxyURL)
		transport.Proxy = trusted.proxyForRequest
		if probe != nil {
			transport.GetProxyConnectHeader = func(context.Context, *url.URL, string) (http.Header, error) {
				probe.proxyConnectStarted()
				return nil, nil
			}
			transport.OnProxyConnectResponse = func(_ context.Context, _ *url.URL, _ *http.Request, response *http.Response) error {
				probe.proxyConnectFinished(response.StatusCode, nil)
				return nil
			}
		}
	case core.EgressModeSOCKS5:
		proxyURL, _ := url.Parse(profile.ProxyEndpoint)
		proxyAddress := proxyURL.Host
		var auth *xproxy.Auth
		if username != "" {
			auth = &xproxy.Auth{User: username, Password: password}
		}
		dialer, err := xproxy.SOCKS5("tcp", proxyAddress, auth, &net.Dialer{})
		if err != nil {
			return nil, err
		}
		contextDialer, ok := dialer.(xproxy.ContextDialer)
		if !ok {
			return nil, ErrUntrustedTransport
		}
		transport.DialContext = trusted.socks5DialContext(proxyAddress, contextDialer)
	default:
		return nil, ErrInvalidProfile
	}
	return &Client{transport: trusted}, nil
}

func proxyBasicCredential(profile core.EgressProfile, credential *core.Credential) (string, string, error) {
	if profile.CredentialID == "" {
		if credential != nil {
			return "", "", ErrInvalidCredential
		}
		return "", "", nil
	}
	if credential == nil || credential.ID != profile.CredentialID || credential.Provider != "egress" || !credential.Enabled || credential.AuthKind != "basic" || credential.Value == nil {
		return "", "", ErrInvalidCredential
	}
	username, password, ok := strings.Cut(*credential.Value, ":")
	if !ok || username == "" || password == "" || strings.Contains(username, ":") || containsControl(username) || containsControl(password) {
		return "", "", ErrInvalidCredential
	}
	return username, password, nil
}

// ValidateCredential 与 Build 复用同一个 parser，供 Router 在发网前完成引用
// 预检；它不返回解析出的用户名或密码。
func ValidateCredential(profile core.EgressProfile, credential *core.Credential) error {
	_, _, err := proxyBasicCredential(profile, credential)
	return err
}

// ValidateHTTPSOrDirectLoopback 保护会发送用户内容的请求：远程目标必须使用
// HTTPS，明文 HTTP 只允许字面 loopback IP 经显式 direct 出口访问。
func ValidateHTTPSOrDirectLoopback(target *url.URL, profile core.EgressProfile) error {
	if target == nil {
		return ErrUntrustedTransport
	}
	if target.Scheme == "https" {
		return nil
	}
	address := net.ParseIP(target.Hostname())
	if target.Scheme != "http" || address == nil || !address.IsLoopback() || profile.Mode != core.EgressModeDirect {
		return ErrUntrustedTransport
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func (transport *trustedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport.base.RoundTrip(request)
}

func (transport *trustedTransport) proxyForRequest(request *http.Request) (*url.URL, error) {
	proxyURL, err := transport.proxy(request)
	if err != nil {
		if transport.probe != nil {
			transport.probe.proxyDecision(nil, err)
		}
		return nil, err
	}
	if proxyURL != nil {
		transport.proxied.Store(true)
	}
	if transport.probe != nil {
		transport.probe.proxyDecision(proxyURL, nil)
	}
	return proxyURL, nil
}

func (transport *trustedTransport) socks5DialContext(proxyAddress string, dialer xproxy.ContextDialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, targetAddress string) (net.Conn, error) {
		transport.proxied.Store(true)
		targetHost, targetPort, err := net.SplitHostPort(targetAddress)
		if err != nil {
			return nil, fmt.Errorf("%w: target address", ErrInvalidProfile)
		}
		if transport.probe != nil {
			transport.probe.proxyDecisionHost(proxyAddress)
		}
		targets := []string{targetAddress}
		if transport.profile.Socks5DNS == core.Socks5DNSLocal && net.ParseIP(targetHost) == nil {
			if transport.probe != nil {
				transport.probe.setDNSSubject(SubjectTarget)
			}
			addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, targetHost)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if len(addresses) == 0 {
				return nil, &net.DNSError{Name: targetHost, Err: "no addresses"}
			}
			targets = make([]string, 0, len(addresses))
			for _, address := range addresses {
				targets = append(targets, net.JoinHostPort(address.IP.String(), targetPort))
			}
		}

		var lastErr error
		for _, destination := range targets {
			if transport.probe != nil {
				transport.probe.setDNSSubject(SubjectProxy)
				transport.probe.proxyConnectStarted()
			}
			connection, dialErr := dialer.DialContext(ctx, network, destination)
			if transport.probe != nil {
				transport.probe.proxyConnectFinished(0, dialErr)
			}
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// HTTPClient 返回已绑定 sealed transport 的新 client；该对象只在 Adapter
// 内部生成，不继承请求方提供的 http.Client 或 RoundTripper。
func (client *Client) HTTPClient() (*http.Client, error) {
	transport, err := client.RoundTripper()
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport}, nil
}

// RoundTripper 只返回 Build 持有的 sealed concrete transport，供 Adapter 在
// 同一出口外层增加签名。它不接受调用方提供的替代 transport。
func (client *Client) RoundTripper() (http.RoundTripper, error) {
	if client == nil || client.transport == nil {
		return nil, ErrUntrustedTransport
	}
	return client.transport, nil
}

// ProxyFor 只允许可信 client 对干净请求执行自己的固定 proxy 决策。RSSHub
// 用它在签名前拒绝可能经过代理的明文 HTTP 请求。
func (client *Client) ProxyFor(request *http.Request) (bool, error) {
	if client == nil || client.transport == nil || request == nil || request.URL == nil {
		return false, ErrUntrustedTransport
	}
	if client.transport.proxy == nil {
		return client.transport.profile.Mode == core.EgressModeSOCKS5, nil
	}
	proxyURL, err := client.transport.proxy(request)
	return proxyURL != nil, err
}

func (client *Client) Proxied() bool {
	return client != nil && client.transport != nil && client.transport.proxied.Load()
}
