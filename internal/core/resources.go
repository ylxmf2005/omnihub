package core

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
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
	Source             string         `json:"source"`
	RouteTemplateID    string         `json:"route_template_id"`
	EndpointProfileID  string         `json:"endpoint_profile_id,omitempty"`
	CredentialID       string         `json:"credential_id,omitempty"`
	Parameters         map[string]any `json:"parameters,omitempty"`
	Priority           int            `json:"priority"`
	FallbackChannelIDs []string       `json:"fallback_channel_ids,omitempty"`
	Enabled            bool           `json:"enabled"`
	Revision           int64          `json:"revision"`
}

type Source struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags,omitempty"`
	Origin  string   `json:"origin"`
	Enabled bool     `json:"enabled"`
}

type Provider struct {
	ID                    string   `json:"id"`
	Capabilities          []string `json:"capabilities"`
	AllowsGlobalDiscovery bool     `json:"allows_global_discovery"`
	Enabled               bool     `json:"enabled"`
}

type EndpointProfile struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider"`
	BaseURL  string         `json:"base_url,omitempty"`
	Trust    string         `json:"trust"`
	Options  map[string]any `json:"options,omitempty"`
	Enabled  bool           `json:"enabled"`
	Revision int64          `json:"revision"`
}

type Collection struct {
	ID         string   `json:"id"`
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
	Revision    int64             `json:"revision"`
	Endpoints   []EndpointProfile `json:"endpoints"`
	Channels    []Channel         `json:"channels"`
	Collections []Collection      `json:"collections"`
	Overlays    []TemplateOverlay `json:"overlays"`
}

// ValidateForStorage 保护 SQLite user snapshot 的自包含不变量。Source、
// RouteTemplate、Credential 可跨升级或删除而悬挂，因此这里只校验无需静态
// Registry 即可裁决的结构；运行时引用由 Registry/Doctor 如实诊断。
func (catalog RoutingCatalog) ValidateForStorage() error {
	if catalog.Revision < 0 {
		return fmt.Errorf("%w: revision must not be negative", ErrInvalidRoutingCatalog)
	}
	_, err := uniqueIDs("endpoint", len(catalog.Endpoints), func(index int) string { return catalog.Endpoints[index].ID })
	if err != nil {
		return err
	}
	channels, err := uniqueIDs("channel", len(catalog.Channels), func(index int) string { return catalog.Channels[index].ID })
	if err != nil {
		return err
	}
	if _, err := uniqueIDs("collection", len(catalog.Collections), func(index int) string { return catalog.Collections[index].ID }); err != nil {
		return err
	}
	if _, err := uniqueIDs("template overlay", len(catalog.Overlays), func(index int) string { return catalog.Overlays[index].RouteTemplateID }); err != nil {
		return err
	}

	for _, endpoint := range catalog.Endpoints {
		if strings.TrimSpace(endpoint.Provider) == "" || containsSensitiveConfig(endpoint.Options) {
			return fmt.Errorf("%w: endpoint %s is incomplete or contains credential material", ErrInvalidRoutingCatalog, endpoint.ID)
		}
	}
	for _, channel := range catalog.Channels {
		if strings.TrimSpace(channel.Source) == "" || strings.TrimSpace(channel.RouteTemplateID) == "" || containsSensitiveConfig(channel.Parameters) {
			return fmt.Errorf("%w: channel %s is incomplete or contains credential material", ErrInvalidRoutingCatalog, channel.ID)
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
		seen := make(map[string]bool)
		for _, channelID := range collection.ChannelIDs {
			if _, ok := channels[channelID]; !ok || seen[channelID] {
				return fmt.Errorf("%w: collection %s has invalid channel %s", ErrInvalidRoutingCatalog, collection.ID, channelID)
			}
			seen[channelID] = true
		}
	}
	return nil
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
	}
	return false
}

func sensitiveConfigKey(key string) bool {
	// 同时拆分 snake/kebab/dotted 与 camelCase，避免 accessToken、
	// clientSecret 等常见配置名绕过只允许引用 Credential 的存储边界。
	var normalized strings.Builder
	for index, current := range key {
		if current >= 'A' && current <= 'Z' {
			if index > 0 {
				previous := rune(key[index-1])
				if previous >= 'a' && previous <= 'z' || previous >= '0' && previous <= '9' {
					normalized.WriteByte('_')
				}
			}
			normalized.WriteRune(current + ('a' - 'A'))
			continue
		}
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' {
			normalized.WriteRune(current)
			continue
		}
		normalized.WriteByte('_')
	}
	parts := strings.FieldsFunc(normalized.String(), func(value rune) bool { return value == '_' })
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
