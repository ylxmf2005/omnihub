package core

import (
	"time"
)

type RouteTemplate struct {
	RouteTemplateID  string               `json:"route_template_id"`
	SourceConstraint SourceConstraint     `json:"source_constraint"`
	Provider         string               `json:"provider"`
	Adapter          string               `json:"adapter"`
	Capabilities     []string             `json:"capabilities"`
	ContentLevel     string               `json:"content_level"`
	Pagination       PaginationDescriptor `json:"pagination"`
	TimeRange        TimeRangeDescriptor  `json:"time_range"`
	Auth             AuthDescriptor       `json:"auth"`
	ParametersSchema map[string]any       `json:"parameters_schema,omitempty"`
	Cost             string               `json:"cost"`
	Trust            string               `json:"trust"`
	Limitations      []string             `json:"limitations,omitempty"`
}

type PaginationDescriptor struct {
	Kind              string `json:"kind"`
	GloballyMergeable bool   `json:"globally_mergeable"`
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
	APIVersion     string          `json:"apiVersion" yaml:"apiVersion"`
	Kind           string          `json:"kind" yaml:"kind"`
	RouteTemplates []RouteTemplate `json:"routeTemplates" yaml:"routeTemplates"`
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
