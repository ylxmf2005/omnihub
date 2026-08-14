package core

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var ErrInvalidRoutingCatalog = errors.New("invalid routing catalog")

type RouteTemplate struct {
	RouteTemplateID  string               `json:"route_template_id" yaml:"route_template_id"`
	Origin           string               `json:"origin" yaml:"origin"`
	SourceConstraint SourceConstraint     `json:"source_constraint" yaml:"source_constraint"`
	Provider         string               `json:"provider"`
	Adapter          string               `json:"adapter"`
	Capabilities     []string             `json:"capabilities"`
	ContentLevel     string               `json:"content_level" yaml:"content_level"`
	Pagination       PaginationDescriptor `json:"pagination"`
	TimeRange        TimeRangeDescriptor  `json:"time_range" yaml:"time_range"`
	Auth             AuthDescriptor       `json:"auth"`
	EndpointRequired bool                 `json:"endpoint_required,omitempty" yaml:"endpoint_required,omitempty"`
	ParametersSchema map[string]any       `json:"parameters_schema,omitempty" yaml:"parameters_schema,omitempty"`
	Cost             string               `json:"cost"`
	Trust            string               `json:"trust"`
	Limitations      []string             `json:"limitations,omitempty"`
}

type PaginationDescriptor struct {
	Kind              string `json:"kind"`
	GloballyMergeable bool   `json:"globally_mergeable" yaml:"globally_mergeable"`
}

type TimeRangeDescriptor struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type SourceConstraint struct {
	Kind   string   `json:"kind"`
	Values []string `json:"values,omitempty"`
}

type AuthDescriptor struct {
	Kind              string       `json:"kind"`
	Required          bool         `json:"required"`
	LoginURL          string       `json:"login_url,omitempty"`
	Browser           string       `json:"browser,omitempty"`
	PermissionOrigins []string     `json:"permission_origins,omitempty"`
	CookieScope       *CookieScope `json:"cookie_scope,omitempty"`
}

type CookieScope struct {
	URL            string   `json:"url"`
	AllowedDomains []string `json:"allowed_domains"`
	Names          []string `json:"names"`
	Store          string   `json:"store"`
	Partitions     []string `json:"partitions"`
}

type Channel struct {
	ID                 string         `json:"id"`
	DisplayName        string         `json:"display_name,omitempty"`
	Source             string         `json:"source"`
	RouteTemplateID    string         `json:"route_template_id"`
	EndpointProfileID  string         `json:"endpoint_profile_id,omitempty"`
	EgressProfileID    string         `json:"egress_profile_id,omitempty"`
	CredentialID       string         `json:"credential_id,omitempty"`
	Parameters         map[string]any `json:"parameters,omitempty"`
	Priority           int            `json:"priority"`
	FallbackChannelIDs []string       `json:"fallback_channel_ids,omitempty"`
	Enabled            bool           `json:"enabled"`
	Revision           int64          `json:"revision"`
	FeedMetadata       *FeedMetadata  `json:"feed_metadata,omitempty"`
}

// FeedMetadata 只保存 OPML 标准订阅字段，不能影响 Adapter 执行或携带凭据。
// 未识别的 OPML attribute 在 Stage 2 import report 中披露后忽略。
type FeedMetadata struct {
	HTMLURL     string `json:"html_url,omitempty"`
	Description string `json:"description,omitempty"`
	Language    string `json:"language,omitempty"`
	Version     string `json:"version,omitempty"`
}

type Source struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"display_name,omitempty"`
	CanonicalURL string   `json:"canonical_url,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Origin       string   `json:"origin"`
	Enabled      bool     `json:"enabled"`
}

type Provider struct {
	ID                    string   `json:"id"`
	Capabilities          []string `json:"capabilities"`
	AllowsGlobalDiscovery bool     `json:"allows_global_discovery"`
	Enabled               bool     `json:"enabled"`
}

type EndpointProfile struct {
	ID              string         `json:"id"`
	Provider        string         `json:"provider"`
	BaseURL         string         `json:"base_url,omitempty"`
	EgressProfileID string         `json:"egress_profile_id,omitempty"`
	Trust           string         `json:"trust"`
	Options         map[string]any `json:"options,omitempty"`
	Enabled         bool           `json:"enabled"`
	Revision        int64          `json:"revision"`
}

type EgressMode string

const (
	EgressModeEnvironment EgressMode = "environment"
	EgressModeDirect      EgressMode = "direct"
	EgressModeHTTPProxy   EgressMode = "http_proxy"
	EgressModeSOCKS5      EgressMode = "socks5"
)

type Socks5DNSMode string

const (
	Socks5DNSLocal Socks5DNSMode = "local"
	Socks5DNSProxy Socks5DNSMode = "proxy"
)

// EgressProfile 是用户显式选择的出站路径。ProxyEndpoint 只进入受信任
// transport 构造，普通列表和执行结果必须使用 EgressProfileSummary。
type EgressProfile struct {
	ID            string        `json:"id"`
	DisplayName   string        `json:"display_name,omitempty"`
	Mode          EgressMode    `json:"mode"`
	ProxyEndpoint string        `json:"proxy_endpoint,omitempty"`
	CredentialID  string        `json:"credential_id,omitempty"`
	Socks5DNS     Socks5DNSMode `json:"socks5_dns,omitempty"`
	Enabled       bool          `json:"enabled"`
	Revision      int64         `json:"revision"`
}

// EgressProfileSummary 是管理面可安全返回的视图。HasEndpoint 只表达
// 已配置代理地址，不回显地址本身；Proxied 表达静态代理模式，不猜测
// environment 对某次请求是否实际命中代理。
type EgressProfileSummary struct {
	ID           string     `json:"id"`
	DisplayName  string     `json:"display_name,omitempty"`
	Mode         EgressMode `json:"mode"`
	Proxied      bool       `json:"proxied"`
	HasEndpoint  bool       `json:"has_endpoint"`
	CredentialID string     `json:"credential_id,omitempty"`
	Enabled      bool       `json:"enabled"`
	Revision     int64      `json:"revision"`
}

type Collection struct {
	ID         string   `json:"id"`
	Title      string   `json:"title,omitempty"`
	ParentID   string   `json:"parent_id,omitempty"`
	Position   int      `json:"position"`
	ChannelIDs []string `json:"channel_ids"`
	Enabled    bool     `json:"enabled"`
	Revision   int64    `json:"revision"`
}

type TemplateOverlay struct {
	RouteTemplateID string `json:"route_template_id"`
	Enabled         bool   `json:"enabled"`
	Trusted         bool   `json:"trusted"`
	Revision        int64  `json:"revision"`
}

type RoutingCatalog struct {
	Revision       int64             `json:"revision"`
	Sources        []Source          `json:"sources"`
	Endpoints      []EndpointProfile `json:"endpoints"`
	EgressProfiles []EgressProfile   `json:"egress_profiles"`
	Channels       []Channel         `json:"channels"`
	Collections    []Collection      `json:"collections"`
	Overlays       []TemplateOverlay `json:"overlays"`
}

// ValidateForStorage 保护 SQLite user snapshot 的自包含不变量。Source、
// RouteTemplate、Credential 可跨升级或删除而悬挂，因此这里只校验无需静态
// Registry 即可裁决的结构；运行时引用由 Registry/Doctor 如实诊断。
func (catalog RoutingCatalog) ValidateForStorage() error {
	if catalog.Revision < 0 {
		return fmt.Errorf("%w: revision must not be negative", ErrInvalidRoutingCatalog)
	}
	_, err := uniqueIDs("source", len(catalog.Sources), func(index int) string { return catalog.Sources[index].ID })
	if err != nil {
		return err
	}
	for _, source := range catalog.Sources {
		if source.Origin != "user" || !validOptionalPublicHTTPURL(source.CanonicalURL) {
			return fmt.Errorf("%w: persisted source %s must be user-owned and contain a safe canonical URL", ErrInvalidRoutingCatalog, source.ID)
		}
	}
	_, err = uniqueIDs("endpoint", len(catalog.Endpoints), func(index int) string { return catalog.Endpoints[index].ID })
	if err != nil {
		return err
	}
	if _, err := uniqueIDs("egress profile", len(catalog.EgressProfiles), func(index int) string { return catalog.EgressProfiles[index].ID }); err != nil {
		return err
	}
	channels, err := uniqueIDs("channel", len(catalog.Channels), func(index int) string { return catalog.Channels[index].ID })
	if err != nil {
		return err
	}
	collections, err := uniqueIDs("collection", len(catalog.Collections), func(index int) string { return catalog.Collections[index].ID })
	if err != nil {
		return err
	}
	if _, err := uniqueIDs("template overlay", len(catalog.Overlays), func(index int) string { return catalog.Overlays[index].RouteTemplateID }); err != nil {
		return err
	}

	for _, endpoint := range catalog.Endpoints {
		if strings.TrimSpace(endpoint.Provider) == "" || !validOptionalPublicHTTPURL(endpoint.BaseURL) || containsSensitiveConfig(endpoint.Options) {
			return fmt.Errorf("%w: endpoint %s is incomplete or contains credential material", ErrInvalidRoutingCatalog, endpoint.ID)
		}
	}
	for _, profile := range catalog.EgressProfiles {
		if err := profile.Validate(); err != nil {
			return err
		}
	}
	for _, channel := range catalog.Channels {
		metadataURLValid := channel.FeedMetadata == nil || validOptionalPublicHTTPURL(channel.FeedMetadata.HTMLURL)
		if strings.TrimSpace(channel.Source) == "" || strings.TrimSpace(channel.RouteTemplateID) == "" || !metadataURLValid || containsSensitiveConfig(channel.Parameters) {
			return fmt.Errorf("%w: channel %s is incomplete or contains credential material", ErrInvalidRoutingCatalog, channel.ID)
		}
		if channel.EndpointProfileID != "" && channel.EgressProfileID != "" {
			return fmt.Errorf("%w: channel %s cannot bind endpoint and egress together", ErrInvalidRoutingCatalog, channel.ID)
		}
		seenFallback := make(map[string]bool)
		for _, fallbackID := range channel.FallbackChannelIDs {
			fallbackIndex, ok := channels[fallbackID]
			if !ok || fallbackID == channel.ID || catalog.Channels[fallbackIndex].Source != channel.Source || seenFallback[fallbackID] {
				return fmt.Errorf("%w: channel %s has invalid fallback %s", ErrInvalidRoutingCatalog, channel.ID, fallbackID)
			}
			seenFallback[fallbackID] = true
		}
	}
	if hasFallbackCycle(catalog.Channels, channels) {
		return fmt.Errorf("%w: fallback graph contains a cycle", ErrInvalidRoutingCatalog)
	}
	for _, collection := range catalog.Collections {
		if collection.ParentID != "" {
			if _, ok := collections[collection.ParentID]; !ok || collection.ParentID == collection.ID {
				return fmt.Errorf("%w: collection %s has invalid parent %s", ErrInvalidRoutingCatalog, collection.ID, collection.ParentID)
			}
		}
		seen := make(map[string]bool)
		for _, channelID := range collection.ChannelIDs {
			if _, ok := channels[channelID]; !ok || seen[channelID] {
				return fmt.Errorf("%w: collection %s has invalid channel %s", ErrInvalidRoutingCatalog, collection.ID, channelID)
			}
			seen[channelID] = true
		}
	}
	if hasCollectionCycle(catalog.Collections, collections) {
		return fmt.Errorf("%w: collection hierarchy contains a cycle", ErrInvalidRoutingCatalog)
	}
	return nil
}

// Validate 校验 EgressProfile 自身的可持久化组合。资源引用可以悬挂，
// 因此 CredentialID 与 Endpoint/Channel 的关联由管理写入和运行时裁决。
func (profile EgressProfile) Validate() error {
	if strings.TrimSpace(profile.ID) == "" {
		return fmt.Errorf("%w: egress profile id is required", ErrInvalidRoutingCatalog)
	}
	switch profile.Mode {
	case EgressModeEnvironment, EgressModeDirect:
		if profile.ProxyEndpoint != "" || profile.CredentialID != "" || profile.Socks5DNS != "" {
			return fmt.Errorf("%w: egress profile %s mode %s cannot contain proxy configuration", ErrInvalidRoutingCatalog, profile.ID, profile.Mode)
		}
	case EgressModeHTTPProxy:
		if profile.Socks5DNS != "" || !validProxyEndpoint(profile.ProxyEndpoint, "http") {
			return fmt.Errorf("%w: egress profile %s must contain an http proxy host and port", ErrInvalidRoutingCatalog, profile.ID)
		}
	case EgressModeSOCKS5:
		if profile.Socks5DNS != Socks5DNSLocal && profile.Socks5DNS != Socks5DNSProxy || !validProxyEndpoint(profile.ProxyEndpoint, "socks5") {
			return fmt.Errorf("%w: egress profile %s must contain a socks5 proxy host, port, and DNS mode", ErrInvalidRoutingCatalog, profile.ID)
		}
	default:
		return fmt.Errorf("%w: egress profile %s has invalid mode %q", ErrInvalidRoutingCatalog, profile.ID, profile.Mode)
	}
	return nil
}

func validProxyEndpoint(value, scheme string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, scheme) || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port <= 65535
}

func uniqueIDs[T ~string](kind string, count int, id func(int) T) (map[string]int, error) {
	values := make(map[string]int, count)
	for index := range count {
		value := strings.TrimSpace(string(id(index)))
		if value == "" {
			return nil, fmt.Errorf("%w: %s id is required", ErrInvalidRoutingCatalog, kind)
		}
		if _, exists := values[value]; exists {
			return nil, fmt.Errorf("%w: duplicate %s id %s", ErrInvalidRoutingCatalog, kind, value)
		}
		values[value] = index
	}
	return values, nil
}

func hasFallbackCycle(channels []Channel, index map[string]int) bool {
	visiting, visited := make(map[string]bool), make(map[string]bool)
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, fallbackID := range channels[index[id]].FallbackChannelIDs {
			if visit(fallbackID) {
				return true
			}
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	for id := range index {
		if visit(id) {
			return true
		}
	}
	return false
}

func hasCollectionCycle(collections []Collection, index map[string]int) bool {
	visiting, visited := make(map[string]bool), make(map[string]bool)
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		parentID := collections[index[id]].ParentID
		if parentID != "" && visit(parentID) {
			return true
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	for id := range index {
		if visit(id) {
			return true
		}
	}
	return false
}

func containsSensitiveConfig(value any) bool {
	return inspectSensitive(reflect.ValueOf(value), "")
}

func inspectSensitive(value reflect.Value, key string) bool {
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		return inspectSensitive(value.Elem(), key)
	}
	switch value.Kind() {
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			name := fmt.Sprint(iterator.Key().Interface())
			if sensitiveConfigKey(name) || inspectSensitive(iterator.Value(), name) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			if inspectSensitive(value.Index(index), key) {
				return true
			}
		}
	case reflect.String:
		return containsSensitiveURL(value.String())
	}
	return false
}

func validOptionalPublicHTTPURL(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	return !parsedURLContainsCredentialMaterial(parsed)
}

func containsSensitiveURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	return parsedURLContainsCredentialMaterial(parsed)
}

func parsedURLContainsCredentialMaterial(parsed *url.URL) bool {
	if parsed.User != nil {
		return true
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return true
	}
	for key := range query {
		if CredentialLikeURLKey(key) {
			return true
		}
	}
	// OAuth 等流程常把 access_token 放在 fragment。普通页面 anchor 不带
	// key=value，仍允许保存；结构化 fragment 则复用与 query 相同的判定。
	if strings.Contains(parsed.Fragment, "=") {
		fragment, fragmentErr := url.ParseQuery(strings.TrimPrefix(parsed.Fragment, "?"))
		if fragmentErr == nil {
			for key := range fragment {
				if CredentialLikeURLKey(key) {
					return true
				}
			}
		}
	}
	return false
}

// CredentialLikeURLKey 是 Feed 执行、用户配置持久化与 OPML 导出共享的
// URL credential 边界。它只判断 query/fragment key，不检查 value。
func CredentialLikeURLKey(key string) bool {
	parts := normalizedConfigKeyParts(key)
	compact := strings.Join(parts, "")
	switch compact {
	case "apikey", "accesskey", "accesstoken", "privatekey":
		return true
	}
	for _, part := range parts {
		switch part {
		case "auth", "authorization", "bearer", "code", "cookie", "credential", "key", "passwd", "password", "secret", "sig", "signature", "token":
			return true
		}
	}
	return false
}

func sensitiveConfigKey(key string) bool {
	// 同时拆分 snake/kebab/dotted 与 camelCase，避免 accessToken、
	// clientSecret 等常见配置名绕过只允许引用 Credential 的存储边界。
	parts := normalizedConfigKeyParts(key)
	compact := strings.Join(parts, "")
	if compact == "apikey" || compact == "accesskey" || compact == "privatekey" {
		return true
	}
	for index, part := range parts {
		switch part {
		case "apikey", "authorization", "bearer", "cookie", "credential", "password", "secret", "token":
			return true
		}
		if index+1 < len(parts) && parts[index+1] == "key" && (part == "api" || part == "access" || part == "private") {
			return true
		}
	}
	return false
}

func normalizedConfigKeyParts(key string) []string {
	var normalized strings.Builder
	characters := []rune(key)
	for index, current := range characters {
		if unicode.IsUpper(current) {
			if index > 0 {
				previous := characters[index-1]
				wordAfterAcronym := unicode.IsUpper(previous) && index+1 < len(characters) && unicode.IsLower(characters[index+1])
				if unicode.IsLower(previous) || unicode.IsDigit(previous) || wordAfterAcronym {
					normalized.WriteByte('_')
				}
			}
			normalized.WriteRune(unicode.ToLower(current))
			continue
		}
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			normalized.WriteRune(unicode.ToLower(current))
			continue
		}
		normalized.WriteByte('_')
	}
	return strings.FieldsFunc(normalized.String(), func(value rune) bool { return value == '_' })
}

type Credential struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	AuthKind  string    `json:"auth_kind"`
	Label     string    `json:"label"`
	Value     *string   `json:"value,omitempty"`
	Enabled   bool      `json:"enabled"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CredentialSummary struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	AuthKind    string `json:"auth_kind"`
	Label       string `json:"label"`
	HasValue    bool   `json:"has_value"`
	ValueMasked string `json:"value_masked,omitempty"`
	Enabled     bool   `json:"enabled"`
	Revision    int64  `json:"revision"`
}

type CredentialInput struct {
	ID       string  `json:"id"`
	Provider string  `json:"provider"`
	AuthKind string  `json:"auth_kind"`
	Label    string  `json:"label"`
	Value    *string `json:"value,omitempty"`
	Enabled  bool    `json:"enabled"`
}

type CredentialDetail struct {
	ID          string  `json:"id"`
	Provider    string  `json:"provider"`
	AuthKind    string  `json:"auth_kind"`
	Label       string  `json:"label"`
	HasValue    bool    `json:"has_value"`
	ValueMasked string  `json:"value_masked,omitempty"`
	Enabled     bool    `json:"enabled"`
	Revision    int64   `json:"revision"`
	Value       *string `json:"value,omitempty"`
}

type BrowserBridge struct {
	ID             string    `json:"id"`
	Browser        string    `json:"browser"`
	Connected      bool      `json:"connected"`
	ProfileLabel   string    `json:"profile_label"`
	GrantedOrigins []string  `json:"granted_origins,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	LastError      *Error    `json:"last_error,omitempty"`
}

type ManagedResource struct {
	ID        string    `json:"id"`
	Origin    string    `json:"origin"`
	Enabled   bool      `json:"enabled"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Bundle struct {
	APIVersion     string                `json:"apiVersion" yaml:"apiVersion"`
	Kind           string                `json:"kind" yaml:"kind"`
	Sources        []BundleSource        `json:"sources,omitempty" yaml:"sources,omitempty"`
	Providers      []BundleProvider      `json:"providers,omitempty" yaml:"providers,omitempty"`
	RouteTemplates []BundleRouteTemplate `json:"routeTemplates" yaml:"routeTemplates"`
}

// BundleSource/BundleProvider 是 imported 文件的输入合同。Origin 由 Loader
// 赋为 imported；AllowsGlobalDiscovery 省略时按 false 处理，避免把运行时
// 归一化字段错误声明成用户必填字段。
type BundleSource struct {
	ID      string   `json:"id" yaml:"id"`
	Tags    []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Enabled bool     `json:"enabled" yaml:"enabled"`
}

type BundleProvider struct {
	ID                    string   `json:"id" yaml:"id"`
	Capabilities          []string `json:"capabilities" yaml:"capabilities"`
	AllowsGlobalDiscovery bool     `json:"allows_global_discovery,omitempty" yaml:"allows_global_discovery,omitempty"`
	Enabled               bool     `json:"enabled" yaml:"enabled"`
}

type BundleRouteTemplate struct {
	RouteTemplateID  string               `json:"route_template_id" yaml:"route_template_id"`
	SourceConstraint SourceConstraint     `json:"source_constraint" yaml:"source_constraint"`
	Provider         string               `json:"provider" yaml:"provider"`
	Adapter          string               `json:"adapter" yaml:"adapter"`
	Capabilities     []string             `json:"capabilities" yaml:"capabilities"`
	ContentLevel     string               `json:"content_level" yaml:"content_level"`
	Pagination       PaginationDescriptor `json:"pagination" yaml:"pagination"`
	TimeRange        TimeRangeDescriptor  `json:"time_range" yaml:"time_range"`
	Auth             AuthDescriptor       `json:"auth" yaml:"auth"`
	EndpointRequired bool                 `json:"endpoint_required,omitempty" yaml:"endpoint_required,omitempty"`
	ParametersSchema map[string]any       `json:"parameters_schema,omitempty" yaml:"parameters_schema,omitempty"`
	Cost             string               `json:"cost" yaml:"cost"`
	Trust            string               `json:"trust" yaml:"trust"`
	Limitations      []string             `json:"limitations,omitempty" yaml:"limitations,omitempty"`
}

type Run struct {
	ID             string      `json:"id"`
	Kind           string      `json:"kind"`
	Resource       ResourceRef `json:"resource"`
	RequestID      string      `json:"request_id"`
	PayloadHash    string      `json:"-"`
	IdempotencyKey string      `json:"idempotency_key"`
	Status         RunStatus   `json:"status"`
	ClaimedBy      string      `json:"claimed_by,omitempty"`
	LeaseExpiresAt *time.Time  `json:"lease_expires_at,omitempty"`
	Attempt        int         `json:"attempt"`
	Progress       RunProgress `json:"progress"`
	Revision       int64       `json:"revision"`
	Result         *Envelope   `json:"result,omitempty"`
	LastError      *Error      `json:"last_error,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	FinishedAt     *time.Time  `json:"finished_at,omitempty"`
}

type ResourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type RunProgress struct {
	ChannelsTotal    int `json:"channels_total"`
	ChannelsFinished int `json:"channels_finished"`
}

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunComplete  RunStatus = "complete"
	RunPartial   RunStatus = "partial"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

type ViewSnapshot struct {
	ID        string    `json:"id"`
	ViewID    string    `json:"view_id"`
	Envelope  []byte    `json:"envelope"`
	CreatedAt time.Time `json:"created_at"`
}

type ChannelCheckpoint struct {
	Key        StateKey  `json:"key"`
	Checkpoint string    `json:"checkpoint"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type StateKey struct {
	ChannelID          string `json:"channel_id"`
	RouteTemplateID    string `json:"route_template_id"`
	EndpointProfileID  string `json:"endpoint_profile_id"`
	ParametersHash     string `json:"parameters_hash"`
	CredentialID       string `json:"credential_id"`
	CredentialRevision int64  `json:"credential_revision"`
}
