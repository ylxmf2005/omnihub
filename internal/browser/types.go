package browser

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	HostName = "com.ylxmf2005.omnihub"
	BridgeID = "chrome_default"
)

type ErrorCode string

const (
	ErrorBrowserUnavailable       ErrorCode = "browser_unavailable"
	ErrorBrowserPermissionMissing ErrorCode = "browser_permission_missing"
	ErrorCookieMissing            ErrorCode = "cookie_missing"
	ErrorBrowserRequestFailed     ErrorCode = "browser_request_failed"
	ErrorScopeInvalid             ErrorCode = "scope_invalid"
	ErrorBridgeAlreadyActive      ErrorCode = "bridge_already_active"
	ErrorProtocol                 ErrorCode = "protocol_error"
)

// BridgeError 保留可机读错误码，同时避免把 Cookie 或原始浏览器响应拼进错误文本。
type BridgeError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (err *BridgeError) Error() string {
	if err == nil {
		return ""
	}
	if err.Message == "" {
		return string(err.Code)
	}
	return string(err.Code) + ": " + err.Message
}

func (err *BridgeError) Is(target error) bool {
	want, ok := target.(*BridgeError)
	return ok && err != nil && err.Code == want.Code
}

var (
	ErrBrowserUnavailable       = &BridgeError{Code: ErrorBrowserUnavailable}
	ErrBrowserPermissionMissing = &BridgeError{Code: ErrorBrowserPermissionMissing}
	ErrCookieMissing            = &BridgeError{Code: ErrorCookieMissing}
	ErrBrowserRequestFailed     = &BridgeError{Code: ErrorBrowserRequestFailed}
	ErrScopeInvalid             = &BridgeError{Code: ErrorScopeInvalid}
	ErrBridgeAlreadyActive      = &BridgeError{Code: ErrorBridgeAlreadyActive}
	ErrProtocol                 = &BridgeError{Code: ErrorProtocol}
)

// Cookie 是 Extension 与当前一次执行之间的短生命周期 DTO；调用方不得持久化它。
type Cookie struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Domain    string `json:"domain"`
	Path      string `json:"path"`
	Store     string `json:"store"`
	Partition string `json:"partition"`
}

func (cookie Cookie) String() string {
	return fmt.Sprintf("Cookie{Name:%q, Value:<redacted>, Domain:%q, Path:%q, Store:%q, Partition:%q}", cookie.Name, cookie.Domain, cookie.Path, cookie.Store, cookie.Partition)
}

func (cookie Cookie) GoString() string { return cookie.String() }

type CookieScope struct {
	URL            string   `json:"url"`
	AllowedDomains []string `json:"allowed_domains"`
	Names          []string `json:"names"`
	Store          string   `json:"store"`
	Partitions     []string `json:"partitions"`
}

type ReadCookiesRequest struct {
	RequestID               string      `json:"request_id"`
	ChannelID               string      `json:"channel_id"`
	PermissionOriginPattern string      `json:"permission_origin_pattern"`
	CookieScope             CookieScope `json:"cookie_scope"`
}

type ReadCookiesResponse struct {
	RequestID string   `json:"request_id"`
	Cookies   []Cookie `json:"cookies"`
}

// DiscourseSearchRequest 是当前唯一允许经 Browser Bridge 发出的网络请求。
// URL 仍由后端生成，但 Host 与 Extension 都会重新锁定 linux.do/search.json，
// 避免把已授权 origin 变成通用浏览器代理。
type DiscourseSearchRequest struct {
	RequestID               string `json:"request_id"`
	ChannelID               string `json:"channel_id"`
	PermissionOriginPattern string `json:"permission_origin_pattern"`
	URL                     string `json:"url"`
}

type DiscourseSearchResponse struct {
	RequestID  string `json:"request_id"`
	HTTPStatus int    `json:"http_status"`
	Body       string `json:"body"`
}

type RevokePermissionRequest struct {
	PermissionOriginPattern string `json:"permission_origin_pattern"`
}

type AuthorizationDescriptor struct {
	LoginURL                string      `json:"login_url"`
	PermissionOriginPattern string      `json:"permission_origin_pattern"`
	CookieScope             CookieScope `json:"cookie_scope"`
}

type Status struct {
	Connected      bool      `json:"connected"`
	ProfileLabel   string    `json:"profile_label"`
	GrantedOrigins []string  `json:"granted_origins"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

type RevokePermissionResponse struct {
	RequestID string `json:"request_id"`
	Status    Status `json:"status"`
}

type InstallResult struct {
	ManifestPath string `json:"manifest_path"`
	RegistryKey  string `json:"registry_key,omitempty"`
}

// ValidateCookies 在值离开 Bridge 前再次验证 Extension 没有扩大请求 scope。
func ValidateCookies(request ReadCookiesRequest, cookies []Cookie) error {
	if err := validateReadCookiesRequest(request); err != nil {
		return err
	}
	if len(cookies) == 0 {
		return bridgeError(ErrorCookieMissing, "required cookies are absent")
	}

	names := make(map[string]bool, len(request.CookieScope.Names))
	for _, name := range request.CookieScope.Names {
		names[name] = false
	}
	domains := stringSet(request.CookieScope.AllowedDomains, normalizeDomain)
	partitions := stringSet(request.CookieScope.Partitions, strings.TrimSpace)
	for _, cookie := range cookies {
		if _, ok := names[cookie.Name]; !ok || cookie.Name == "" {
			return bridgeError(ErrorScopeInvalid, "browser returned a cookie name outside the requested scope")
		}
		if cookie.Value == "" {
			return bridgeError(ErrorCookieMissing, "required cookie value is empty")
		}
		if !validCookieDomain(cookie.Domain) {
			return bridgeError(ErrorScopeInvalid, "browser returned invalid cookie domain metadata")
		}
		if _, ok := domains[normalizeDomain(cookie.Domain)]; !ok {
			return bridgeError(ErrorScopeInvalid, "browser returned a cookie domain outside the requested scope")
		}
		if cookie.Path == "" || !strings.HasPrefix(cookie.Path, "/") || strings.ContainsFunc(cookie.Path, unicode.IsControl) || cookie.Store != request.CookieScope.Store {
			return bridgeError(ErrorScopeInvalid, "browser returned cookie metadata outside the requested scope")
		}
		if _, ok := partitions[cookie.Partition]; !ok {
			return bridgeError(ErrorScopeInvalid, "browser returned a cookie partition outside the requested scope")
		}
		names[cookie.Name] = true
	}
	for _, present := range names {
		if !present {
			return bridgeError(ErrorCookieMissing, "one or more required cookies are absent")
		}
	}
	return nil
}

func validateReadCookiesRequest(request ReadCookiesRequest) error {
	if err := validateRequestID(request.RequestID); err != nil {
		return err
	}
	if strings.TrimSpace(request.ChannelID) == "" || request.ChannelID != strings.TrimSpace(request.ChannelID) {
		return bridgeError(ErrorScopeInvalid, "channel_id is required")
	}
	permissionHost, err := parsePermissionPattern(request.PermissionOriginPattern)
	if err != nil {
		return err
	}
	scopeURL, err := url.Parse(request.CookieScope.URL)
	if err != nil || scopeURL.Scheme != "https" || scopeURL.Hostname() == "" || scopeURL.Host != scopeURL.Hostname() || scopeURL.Hostname() != strings.ToLower(scopeURL.Hostname()) || scopeURL.User != nil || scopeURL.RawQuery != "" || scopeURL.Fragment != "" {
		return bridgeError(ErrorScopeInvalid, "cookie scope URL must be an HTTPS URL")
	}
	if !hostMatches(scopeURL.Hostname(), permissionHost) {
		return bridgeError(ErrorScopeInvalid, "cookie scope URL is outside the permission origin")
	}
	if len(request.CookieScope.AllowedDomains) == 0 || len(request.CookieScope.Names) == 0 {
		return bridgeError(ErrorScopeInvalid, "cookie domains and names are required")
	}
	for _, domain := range request.CookieScope.AllowedDomains {
		normalized := normalizeDomain(domain)
		if !validCookieDomain(domain) || normalized == "" || !hostMatches(normalized, permissionHost) {
			return bridgeError(ErrorScopeInvalid, "allowed cookie domain is outside the permission origin")
		}
	}
	seenNames := make(map[string]struct{}, len(request.CookieScope.Names))
	for _, name := range request.CookieScope.Names {
		if name == "" || name != strings.TrimSpace(name) || strings.ContainsFunc(name, unicode.IsControl) {
			return bridgeError(ErrorScopeInvalid, "cookie names must be explicit")
		}
		if _, exists := seenNames[name]; exists {
			return bridgeError(ErrorScopeInvalid, "cookie names must be unique")
		}
		seenNames[name] = struct{}{}
	}
	if request.CookieScope.Store != "current" {
		return bridgeError(ErrorScopeInvalid, "v1 only supports the current Chrome cookie store")
	}
	if len(request.CookieScope.Partitions) != 1 || request.CookieScope.Partitions[0] != "unpartitioned" {
		return bridgeError(ErrorScopeInvalid, "v1 only supports unpartitioned cookies")
	}
	return nil
}

func validateDiscourseSearchRequest(request DiscourseSearchRequest) error {
	if err := validateRequestID(request.RequestID); err != nil {
		return err
	}
	if strings.TrimSpace(request.ChannelID) == "" || request.ChannelID != strings.TrimSpace(request.ChannelID) {
		return bridgeError(ErrorScopeInvalid, "channel_id is required")
	}
	permissionHost, err := parsePermissionPattern(request.PermissionOriginPattern)
	if err != nil {
		return err
	}
	target, err := url.Parse(request.URL)
	if err != nil || target.Scheme != "https" || target.Host != "linux.do" || target.Hostname() != permissionHost || target.Path != "/search.json" || target.User != nil || target.Fragment != "" || len(request.URL) > 8192 {
		return bridgeError(ErrorScopeInvalid, "browser search is limited to https://linux.do/search.json")
	}
	query := target.Query()
	page, pageErr := strconv.Atoi(query.Get("page"))
	if len(query) != 2 || len(query["q"]) != 1 || strings.TrimSpace(query.Get("q")) == "" || len(query.Get("q")) > 4096 || len(query["page"]) != 1 || pageErr != nil || page < 1 || page > 10 || strconv.Itoa(page) != query.Get("page") {
		return bridgeError(ErrorScopeInvalid, "browser search query is outside the supported Discourse scope")
	}
	return nil
}

func validateDiscourseSearchResponse(response DiscourseSearchResponse) error {
	if response.HTTPStatus < 100 || response.HTTPStatus > 599 {
		return bridgeError(ErrorProtocol, "Chrome returned an invalid HTTP status")
	}
	if len(response.Body) > maxBrowserResponseBytes {
		return bridgeError(ErrorProtocol, "Chrome returned an oversized response")
	}
	return nil
}

func parsePermissionPattern(pattern string) (string, error) {
	if pattern == "<all_urls>" {
		return "", bridgeError(ErrorScopeInvalid, "broad browser permission is not allowed")
	}
	parsed, err := url.Parse(pattern)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Path != "/*" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", bridgeError(ErrorScopeInvalid, "permission origin pattern is invalid")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.Contains(host, "*") || parsed.Host != parsed.Hostname() || parsed.Hostname() != host {
		return "", bridgeError(ErrorScopeInvalid, "permission origin must name one exact host")
	}
	return host, nil
}

func validateGrantedOrigins(origins []string) error {
	seen := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if _, err := parsePermissionPattern(origin); err != nil {
			return err
		}
		if _, exists := seen[origin]; exists {
			return bridgeError(ErrorProtocol, "granted origins must be unique")
		}
		seen[origin] = struct{}{}
	}
	return nil
}

func normalizedOrigins(origins []string) []string {
	result := append([]string{}, origins...)
	sort.Strings(result)
	return result
}

func hostMatches(candidate, permissionHost string) bool {
	return normalizeDomain(candidate) == permissionHost
}

func normalizeDomain(domain string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
}

func validCookieDomain(domain string) bool {
	trimmed := strings.TrimSpace(domain)
	return domain == trimmed && domain == strings.ToLower(domain) && !strings.HasPrefix(domain, "..") && !strings.ContainsAny(domain, "/:@?#") && !strings.ContainsFunc(domain, unicode.IsControl)
}

func stringSet(values []string, normalize func(string) string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[normalize(value)] = struct{}{}
	}
	return set
}

func bridgeError(code ErrorCode, message string) *BridgeError {
	return &BridgeError{Code: code, Message: message}
}
