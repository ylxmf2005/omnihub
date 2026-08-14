package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
)

type Layer string

const (
	LayerDNS          Layer = "dns"
	LayerTCP          Layer = "tcp"
	LayerProxyConnect Layer = "proxy_connect"
	LayerTLS          Layer = "tls"
	LayerHTTP         Layer = "http"
	LayerFeedParse    Layer = "feed_parse"
)

type Subject string

const (
	SubjectTarget Subject = "target"
	SubjectProxy  Subject = "proxy"
)

type CheckStatus string

const (
	CheckPassed   CheckStatus = "passed"
	CheckDegraded CheckStatus = "degraded"
	CheckFailed   CheckStatus = "failed"
	CheckNotRun   CheckStatus = "not_run"
)

type Check struct {
	Layer       Layer       `json:"layer"`
	Subject     Subject     `json:"subject"`
	Status      CheckStatus `json:"status"`
	DurationMS  int64       `json:"duration_ms"`
	Reason      string      `json:"reason,omitempty"`
	Retryable   bool        `json:"retryable"`
	ResolvedIPs []string    `json:"resolved_ips,omitempty"`
	Address     string      `json:"address,omitempty"`
}

type ProbeReport struct {
	CheckedAt time.Time            `json:"checked_at"`
	Egress    core.ExecutionEgress `json:"egress"`
	Checks    []Check              `json:"checks"`
}

// Probe 收集一次真实 Feed 请求的 httptrace。它不发额外请求，也不推断未发生
// 的网络层；Report 只在完成后把依赖失败之后的层补为 not_run。
type Probe struct {
	mu sync.Mutex

	profile      core.EgressProfile
	targetScheme string
	targetHost   string
	proxyHost    string
	proxied      bool
	checks       map[string]Check
	starts       map[string]time.Time
	dnsSubject   Subject
	dnsCurrent   Subject
	httpStarted  time.Time
}

func NewProbe(profile core.EgressProfile, target string) *Probe {
	parsed, _ := url.Parse(target)
	return &Probe{
		profile:      profile,
		targetScheme: strings.ToLower(parsed.Scheme),
		targetHost:   strings.ToLower(parsed.Hostname()),
		checks:       make(map[string]Check),
		starts:       make(map[string]time.Time),
	}
}

func (probe *Probe) Context(ctx context.Context) context.Context {
	if probe == nil {
		return ctx
	}
	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			probe.mu.Lock()
			subject := probe.subjectForHost(info.Host)
			if probe.dnsSubject != "" {
				subject = probe.dnsSubject
			}
			probe.dnsCurrent = subject
			probe.starts[checkKey(LayerDNS, subject)] = time.Now()
			probe.mu.Unlock()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			probe.mu.Lock()
			subject := probe.dnsCurrent
			if subject == "" {
				subject = SubjectTarget
			}
			addresses := make([]string, 0, len(info.Addrs))
			for _, address := range info.Addrs {
				addresses = append(addresses, address.IP.String())
			}
			sort.Strings(addresses)
			if info.Err != nil {
				probe.finishLocked(LayerDNS, subject, CheckFailed, "dns_lookup_failed", true, nil, "")
			} else {
				probe.finishLocked(LayerDNS, subject, CheckPassed, "", false, addresses, "")
			}
			probe.mu.Unlock()
		},
		ConnectStart: func(_, address string) {
			probe.mu.Lock()
			subject := probe.connectSubject(address)
			probe.starts[checkKey(LayerTCP, subject)] = time.Now()
			probe.mu.Unlock()
		},
		ConnectDone: func(_, address string, err error) {
			probe.mu.Lock()
			subject := probe.connectSubject(address)
			if err != nil {
				probe.finishLocked(LayerTCP, subject, CheckFailed, "connection_failed", true, nil, cleanAddress(address))
			} else {
				probe.finishLocked(LayerTCP, subject, CheckPassed, "", false, nil, cleanAddress(address))
			}
			probe.mu.Unlock()
		},
		TLSHandshakeStart: func() {
			probe.mu.Lock()
			probe.starts[checkKey(LayerTLS, SubjectTarget)] = time.Now()
			probe.mu.Unlock()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			probe.mu.Lock()
			if err != nil {
				reason, retryable := tlsFailure(err)
				probe.finishLocked(LayerTLS, SubjectTarget, CheckFailed, reason, retryable, nil, "")
			} else {
				probe.finishLocked(LayerTLS, SubjectTarget, CheckPassed, "", false, nil, "")
			}
			probe.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			probe.mu.Lock()
			probe.httpStarted = time.Now()
			probe.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, trace)
}

func (probe *Probe) proxyDecision(proxyURL *url.URL, err error) {
	if probe == nil {
		return
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if err != nil {
		probe.proxied = true
		probe.checks[checkKey(LayerDNS, SubjectProxy)] = Check{Layer: LayerDNS, Subject: SubjectProxy, Status: CheckFailed, Reason: "proxy_resolution_failed", Retryable: false}
		return
	}
	if proxyURL != nil {
		probe.proxied = true
		probe.proxyHost = strings.ToLower(proxyURL.Hostname())
	}
}

func (probe *Probe) proxyDecisionHost(address string) {
	if probe == nil {
		return
	}
	host, _, _ := net.SplitHostPort(address)
	probe.mu.Lock()
	probe.proxied = true
	probe.proxyHost = strings.ToLower(host)
	probe.mu.Unlock()
}

func (probe *Probe) setDNSSubject(subject Subject) {
	if probe == nil {
		return
	}
	probe.mu.Lock()
	probe.dnsSubject = subject
	probe.mu.Unlock()
}

func (probe *Probe) proxyConnectStarted() {
	if probe == nil {
		return
	}
	probe.mu.Lock()
	probe.starts[checkKey(LayerProxyConnect, SubjectTarget)] = time.Now()
	probe.mu.Unlock()
}

func (probe *Probe) proxyConnectFinished(status int, err error) {
	if probe == nil {
		return
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if err != nil {
		probe.finishLocked(LayerProxyConnect, SubjectTarget, CheckFailed, "proxy_handshake_failed", true, nil, "")
		return
	}
	if status == http.StatusProxyAuthRequired {
		probe.finishLocked(LayerProxyConnect, SubjectTarget, CheckFailed, "proxy_auth_required", false, nil, "")
		return
	}
	if status != 0 && (status < 200 || status >= 300) {
		probe.finishLocked(LayerProxyConnect, SubjectTarget, CheckFailed, "proxy_connect_rejected", status >= 500, nil, "")
		return
	}
	probe.finishLocked(LayerProxyConnect, SubjectTarget, CheckPassed, "", false, nil, "")
}

func (probe *Probe) RecordHTTP(status int) {
	if probe == nil {
		return
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.starts[checkKey(LayerHTTP, SubjectTarget)] = probe.httpStarted
	if status >= 200 && status < 400 {
		probe.finishLocked(LayerHTTP, SubjectTarget, CheckPassed, "", false, nil, "")
		return
	}
	reason, retryable := "http_error", false
	switch status {
	case http.StatusUnauthorized:
		reason = "http_unauthorized"
	case http.StatusForbidden:
		reason = "http_forbidden"
	case http.StatusNotFound:
		reason = "http_not_found"
	case http.StatusTooManyRequests:
		reason, retryable = "http_rate_limited", true
	default:
		if status >= 500 {
			reason, retryable = "http_upstream_error", true
		}
	}
	probe.finishLocked(LayerHTTP, SubjectTarget, CheckFailed, reason, retryable, nil, "")
}

func (probe *Probe) RecordFeedParse(err error, started time.Time) {
	if probe == nil {
		return
	}
	probe.mu.Lock()
	probe.starts[checkKey(LayerFeedParse, SubjectTarget)] = started
	if err != nil {
		probe.finishLocked(LayerFeedParse, SubjectTarget, CheckFailed, "invalid_feed", false, nil, "")
	} else {
		probe.finishLocked(LayerFeedParse, SubjectTarget, CheckPassed, "", false, nil, "")
	}
	probe.mu.Unlock()
}

func (probe *Probe) RecordRequestError(err error) {
	if probe == nil || err == nil {
		return
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	for _, check := range probe.checks {
		if check.Status == CheckFailed {
			return
		}
	}
	for _, expected := range probe.expectedLocked() {
		key := checkKey(expected.Layer, expected.Subject)
		if _, exists := probe.checks[key]; exists || expected.Reason == "not_required" || expected.Reason == "delegated_to_egress" || expected.Reason == "ip_literal" {
			continue
		}
		reason, retryable := failureForLayer(expected.Layer, err)
		probe.finishLocked(expected.Layer, expected.Subject, CheckFailed, reason, retryable, nil, "")
		break
	}
}

func (probe *Probe) RecordConfigurationError() {
	if probe == nil {
		return
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	for _, expected := range probe.expectedLocked() {
		if expected.Reason == "not_required" || expected.Reason == "delegated_to_egress" || expected.Reason == "ip_literal" {
			continue
		}
		probe.checks[checkKey(expected.Layer, expected.Subject)] = Check{Layer: expected.Layer, Subject: expected.Subject, Status: CheckFailed, Reason: "invalid_egress_configuration", Retryable: false}
		return
	}
}

func (probe *Probe) Report() ProbeReport {
	if probe == nil {
		return ProbeReport{}
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	expected := probe.expectedLocked()
	checks := make([]Check, 0, len(expected))
	blocked := false
	for _, placeholder := range expected {
		key := checkKey(placeholder.Layer, placeholder.Subject)
		check, exists := probe.checks[key]
		if !exists {
			check = placeholder
			if blocked && check.Reason == "" {
				check.Status = CheckNotRun
				check.Reason = "prerequisite_failed"
			} else if check.Reason == "" {
				check.Status = CheckDegraded
				check.Reason = "observation_unavailable"
			}
		}
		checks = append(checks, check)
		if check.Status == CheckFailed {
			blocked = true
		}
	}
	return ProbeReport{
		CheckedAt: time.Now().UTC(),
		Egress:    core.ExecutionEgress{ProfileID: probe.profile.ID, Mode: probe.profile.Mode, Proxied: probe.proxied},
		Checks:    checks,
	}
}

func (probe *Probe) expectedLocked() []Check {
	notRun := func(layer Layer, subject Subject, reason string) Check {
		return Check{Layer: layer, Subject: subject, Status: CheckNotRun, Reason: reason}
	}
	active := func(layer Layer, subject Subject) Check {
		return Check{Layer: layer, Subject: subject}
	}
	tlsCheck := active(LayerTLS, SubjectTarget)
	if probe.targetScheme != "https" {
		tlsCheck = notRun(LayerTLS, SubjectTarget, "not_required")
	}

	proxied := probe.proxied || probe.profile.Mode == core.EgressModeHTTPProxy || probe.profile.Mode == core.EgressModeSOCKS5
	if !proxied {
		dns := active(LayerDNS, SubjectTarget)
		if net.ParseIP(probe.targetHost) != nil {
			dns = notRun(LayerDNS, SubjectTarget, "ip_literal")
		}
		return []Check{
			dns,
			active(LayerTCP, SubjectTarget),
			notRun(LayerProxyConnect, SubjectTarget, "not_required"),
			tlsCheck,
			active(LayerHTTP, SubjectTarget),
			active(LayerFeedParse, SubjectTarget),
		}
	}

	targetDNS := notRun(LayerDNS, SubjectTarget, "delegated_to_egress")
	if probe.profile.Mode == core.EgressModeSOCKS5 && probe.profile.Socks5DNS == core.Socks5DNSLocal {
		targetDNS = active(LayerDNS, SubjectTarget)
		if net.ParseIP(probe.targetHost) != nil {
			targetDNS = notRun(LayerDNS, SubjectTarget, "ip_literal")
		}
	}
	proxyDNS := active(LayerDNS, SubjectProxy)
	if net.ParseIP(probe.proxyHost) != nil {
		proxyDNS = notRun(LayerDNS, SubjectProxy, "ip_literal")
	}
	proxyConnect := active(LayerProxyConnect, SubjectTarget)
	if probe.profile.Mode != core.EgressModeSOCKS5 && probe.targetScheme != "https" {
		proxyConnect = notRun(LayerProxyConnect, SubjectTarget, "not_required")
	}
	return []Check{
		targetDNS,
		proxyDNS,
		active(LayerTCP, SubjectProxy),
		proxyConnect,
		tlsCheck,
		active(LayerHTTP, SubjectTarget),
		active(LayerFeedParse, SubjectTarget),
	}
}

func (probe *Probe) subjectForHost(host string) Subject {
	if probe.proxyHost != "" && strings.EqualFold(host, probe.proxyHost) {
		return SubjectProxy
	}
	return SubjectTarget
}

func (probe *Probe) connectSubject(address string) Subject {
	if probe.proxied || probe.profile.Mode == core.EgressModeHTTPProxy || probe.profile.Mode == core.EgressModeSOCKS5 {
		return SubjectProxy
	}
	return SubjectTarget
}

func (probe *Probe) finishLocked(layer Layer, subject Subject, status CheckStatus, reason string, retryable bool, resolvedIPs []string, address string) {
	key := checkKey(layer, subject)
	duration := int64(0)
	if started := probe.starts[key]; !started.IsZero() {
		duration = time.Since(started).Milliseconds()
	}
	probe.checks[key] = Check{Layer: layer, Subject: subject, Status: status, DurationMS: duration, Reason: reason, Retryable: retryable, ResolvedIPs: resolvedIPs, Address: address}
}

func checkKey(layer Layer, subject Subject) string {
	return string(layer) + "\x00" + string(subject)
}

func cleanAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	if parsed := net.ParseIP(host); parsed != nil {
		host = parsed.String()
	}
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return net.JoinHostPort(host, port)
}

func tlsFailure(err error) (string, bool) {
	var certificateError x509.CertificateInvalidError
	var hostnameError x509.HostnameError
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &certificateError) || errors.As(err, &hostnameError) || errors.As(err, &unknownAuthority) {
		return "certificate_invalid", false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "handshake_timeout", true
	}
	return "tls_handshake_failed", true
}

func failureForLayer(layer Layer, err error) (string, bool) {
	var networkError net.Error
	timedOut := errors.As(err, &networkError) && networkError.Timeout() || errors.Is(err, context.DeadlineExceeded)
	switch layer {
	case LayerDNS:
		return "dns_lookup_failed", true
	case LayerTCP:
		return "connection_failed", true
	case LayerProxyConnect:
		return "proxy_handshake_failed", true
	case LayerTLS:
		return tlsFailure(err)
	case LayerHTTP:
		if timedOut {
			return "http_timeout", true
		}
		return "http_request_failed", true
	default:
		return "request_failed", timedOut
	}
}
