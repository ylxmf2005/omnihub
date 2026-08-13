package core

import (
	"time"
)

const SchemaVersion = "1.0"

// Operation 是所有查询出口共享的输入模型。
type Operation struct {
	SchemaVersion      string             `json:"schema_version" jsonschema:"合同版本，当前为 1.0。"`
	Operation          OperationKind      `json:"operation" jsonschema:"执行类型：search、latest 或 fetch。"`
	Query              *string            `json:"query,omitempty" jsonschema:"search 查询词；其他操作省略。"`
	Target             *string            `json:"target,omitempty" jsonschema:"fetch 目标 URL 或上游标识；其他操作省略。"`
	Scope              Scope              `json:"scope" jsonschema:"允许执行的 Channel、Source、Provider、Domain 或 Collection 范围。"`
	RoutePolicy        RoutePolicy        `json:"route_policy" jsonschema:"Channel 选择、排除、聚合和回退策略。"`
	Limit              int                `json:"limit" jsonschema:"每次执行希望返回的最大条目数。"`
	TimeRange          TimeRange          `json:"time_range" jsonschema:"可选的 UTC 时间范围。"`
	IdentityDedupe     IdentityDedupe     `json:"identity_dedupe" jsonschema:"身份去重策略。"`
	SimilarityGrouping SimilarityGrouping `json:"similarity_grouping" jsonschema:"相似内容仅分组，不删除 Item。"`
	Continuation       *string            `json:"continuation,omitempty" jsonschema:"OmniHub 签发的不透明续页 token。"`
	DeadlineMS         int                `json:"deadline_ms" jsonschema:"整次 Operation 的毫秒 deadline。"`
}

// SearchInput/LatestInput/FetchInput 让各公共出口在 Schema 层固定各自条件必填；进入 Core 后统一转为 Operation。
type SearchInput struct {
	SchemaVersion      string             `json:"schema_version"`
	Query              string             `json:"query"`
	Scope              Scope              `json:"scope"`
	RoutePolicy        RoutePolicy        `json:"route_policy"`
	Limit              int                `json:"limit"`
	TimeRange          TimeRange          `json:"time_range"`
	IdentityDedupe     IdentityDedupe     `json:"identity_dedupe"`
	SimilarityGrouping SimilarityGrouping `json:"similarity_grouping"`
	Continuation       *string            `json:"continuation,omitempty"`
	DeadlineMS         int                `json:"deadline_ms"`
}

func (input SearchInput) OperationRequest() Operation {
	return Operation{SchemaVersion: input.SchemaVersion, Operation: OperationSearch, Query: &input.Query, Scope: input.Scope, RoutePolicy: input.RoutePolicy, Limit: input.Limit, TimeRange: input.TimeRange, IdentityDedupe: input.IdentityDedupe, SimilarityGrouping: input.SimilarityGrouping, Continuation: input.Continuation, DeadlineMS: input.DeadlineMS}
}

type LatestInput struct {
	SchemaVersion      string             `json:"schema_version"`
	Scope              Scope              `json:"scope"`
	RoutePolicy        RoutePolicy        `json:"route_policy"`
	Limit              int                `json:"limit"`
	TimeRange          TimeRange          `json:"time_range"`
	IdentityDedupe     IdentityDedupe     `json:"identity_dedupe"`
	SimilarityGrouping SimilarityGrouping `json:"similarity_grouping"`
	Continuation       *string            `json:"continuation,omitempty"`
	DeadlineMS         int                `json:"deadline_ms"`
}

func (input LatestInput) OperationRequest() Operation {
	return Operation{SchemaVersion: input.SchemaVersion, Operation: OperationLatest, Scope: input.Scope, RoutePolicy: input.RoutePolicy, Limit: input.Limit, TimeRange: input.TimeRange, IdentityDedupe: input.IdentityDedupe, SimilarityGrouping: input.SimilarityGrouping, Continuation: input.Continuation, DeadlineMS: input.DeadlineMS}
}

type FetchInput struct {
	SchemaVersion string      `json:"schema_version"`
	Target        string      `json:"target"`
	Scope         Scope       `json:"scope"`
	RoutePolicy   RoutePolicy `json:"route_policy"`
	DeadlineMS    int         `json:"deadline_ms"`
}

func (input FetchInput) OperationRequest() Operation {
	return Operation{SchemaVersion: input.SchemaVersion, Operation: OperationFetch, Target: &input.Target, Scope: input.Scope, RoutePolicy: input.RoutePolicy, Limit: 1, IdentityDedupe: IdentityExact, SimilarityGrouping: SimilarityOff, DeadlineMS: input.DeadlineMS}
}

type OperationKind string

const (
	OperationSearch OperationKind = "search"
	OperationLatest OperationKind = "latest"
	OperationFetch  OperationKind = "fetch"
)

type Scope struct {
	Channels   []string `json:"channels,omitempty"`
	Sources    []string `json:"sources,omitempty"`
	Providers  []string `json:"providers,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	Collection *string  `json:"collection,omitempty"`
}

type RoutePolicy struct {
	Mode          RouteMode       `json:"mode"`
	Prefer        []RouteSelector `json:"prefer,omitempty"`
	Only          []RouteSelector `json:"only,omitempty"`
	Exclude       []RouteSelector `json:"exclude,omitempty"`
	Aggregate     bool            `json:"aggregate"`
	AllowFallback bool            `json:"allow_fallback"`
}

type RouteMode string

const (
	RouteAuto    RouteMode = "auto"
	RoutePrefer  RouteMode = "prefer"
	RouteOnly    RouteMode = "only"
	RouteExclude RouteMode = "exclude"
)

type RouteSelector struct {
	Kind SelectorKind `json:"kind"`
	ID   string       `json:"id"`
}

type SelectorKind string

const (
	SelectorChannel  SelectorKind = "channel"
	SelectorProvider SelectorKind = "provider"
)

type TimeRange struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

type IdentityDedupe string

const (
	IdentityNone  IdentityDedupe = "none"
	IdentityExact IdentityDedupe = "exact"
)

type SimilarityGrouping string

const (
	SimilarityOff     SimilarityGrouping = "off"
	SimilarityTitle   SimilarityGrouping = "title"
	SimilarityContent SimilarityGrouping = "content"
)

// Envelope 是同步执行和持久 Run 终态共享的结果模型。
type Envelope struct {
	SchemaVersion string       `json:"schema_version"`
	RequestID     string       `json:"request_id"`
	Status        Status       `json:"status"`
	Request       Operation    `json:"request"`
	Executions    []Execution  `json:"executions"`
	Items         []Item       `json:"items"`
	Coverage      []Coverage   `json:"coverage"`
	Errors        []Error      `json:"errors"`
	Continuation  Continuation `json:"continuation"`
	Meta          Meta         `json:"meta"`
}

type Status string

const (
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusFailed   Status = "failed"
)

type Execution struct {
	ChannelID       string          `json:"channel_id"`
	RouteTemplateID string          `json:"route_template_id"`
	Source          string          `json:"source"`
	Provider        string          `json:"provider"`
	Endpoint        string          `json:"endpoint,omitempty"`
	Capability      string          `json:"capability"`
	Selection       string          `json:"selection"`
	Status          ExecutionStatus `json:"status"`
	Reason          *string         `json:"reason,omitempty"`
	StartedAt       time.Time       `json:"started_at"`
	DurationMS      int64           `json:"duration_ms"`
	Examined        int             `json:"examined"`
	Returned        int             `json:"returned"`
	Auth            ExecutionAuth   `json:"auth"`
	Limitations     []string        `json:"limitations,omitempty"`
}

type ExecutionAuth struct {
	Required     bool   `json:"required"`
	Used         bool   `json:"used"`
	CredentialID string `json:"credential_id,omitempty"`
}

type ExecutionStatus string

const (
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionSkipped   ExecutionStatus = "skipped"
)

type Item struct {
	ID           string         `json:"id"`
	URL          string         `json:"url"`
	ExternalURL  *string        `json:"external_url,omitempty"`
	Title        string         `json:"title,omitempty"`
	Content      Content        `json:"content"`
	Summary      *string        `json:"summary,omitempty"`
	Image        *string        `json:"image,omitempty"`
	BannerImage  *string        `json:"banner_image,omitempty"`
	PublishedAt  *time.Time     `json:"published_at,omitempty"`
	ModifiedAt   *time.Time     `json:"modified_at,omitempty"`
	Authors      []Author       `json:"authors,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
	Language     *string        `json:"language,omitempty"`
	Attachments  []Attachment   `json:"attachments,omitempty"`
	Metrics      map[string]any `json:"metrics,omitempty"`
	Observations []Observation  `json:"observations"`
	Identity     Identity       `json:"identity"`
	Similarity   Similarity     `json:"similarity"`
}

type Author struct {
	Name   string  `json:"name"`
	URL    *string `json:"url,omitempty"`
	Avatar *string `json:"avatar,omitempty"`
}

type Attachment struct {
	URL      string  `json:"url"`
	MIMEType *string `json:"mime_type,omitempty"`
	Title    *string `json:"title,omitempty"`
}

type Identity struct {
	ClusterID string `json:"cluster_id"`
	Reason    string `json:"reason"`
}

type Similarity struct {
	GroupID  *string `json:"group_id,omitempty"`
	Strategy string  `json:"strategy"`
}

type Content struct {
	Role           ContentRole `json:"role"`
	Text           *string     `json:"text,omitempty"`
	HTML           *string     `json:"html,omitempty"`
	SourceSupplied bool        `json:"source_supplied"`
}

type ContentRole string

const (
	ContentSnippet ContentRole = "snippet"
	ContentSummary ContentRole = "summary"
	ContentBody    ContentRole = "body"
)

type Verification string

const (
	VerificationCandidate Verification = "candidate"
	VerificationMetadata  Verification = "metadata"
	VerificationBody      Verification = "body"
)

type Observation struct {
	Source          string       `json:"source"`
	Provider        string       `json:"provider"`
	ChannelID       string       `json:"channel_id"`
	RouteTemplateID string       `json:"route_template_id"`
	Endpoint        string       `json:"endpoint,omitempty"`
	UpstreamID      *string      `json:"upstream_id,omitempty"`
	OriginalURL     string       `json:"original_url"`
	CanonicalURL    string       `json:"canonical_url,omitempty"`
	RetrievedAt     time.Time    `json:"retrieved_at"`
	Rank            *int         `json:"rank,omitempty"`
	Score           *float64     `json:"score,omitempty"`
	Verification    Verification `json:"verification"`
	Limitations     []string     `json:"limitations,omitempty"`
}

type Coverage struct {
	Source          string     `json:"source"`
	ChannelID       string     `json:"channel_id"`
	RouteTemplateID string     `json:"route_template_id"`
	Scope           string     `json:"scope"`
	From            *time.Time `json:"from,omitempty"`
	To              *time.Time `json:"to,omitempty"`
	Examined        *int       `json:"examined,omitempty"`
	Returned        *int       `json:"returned,omitempty"`
	Exhaustive      *bool      `json:"exhaustive,omitempty"`
	Truncated       bool       `json:"truncated"`
	Limitations     []string   `json:"limitations,omitempty"`
}

type Error struct {
	Code            string         `json:"code"`
	Message         string         `json:"message"`
	Source          string         `json:"source,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	ChannelID       string         `json:"channel_id,omitempty"`
	RouteTemplateID string         `json:"route_template_id,omitempty"`
	Retryable       bool           `json:"retryable"`
	RetryAfterMS    *int           `json:"retry_after_ms,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
}

type Continuation struct {
	Token       *string  `json:"token,omitempty"`
	Mode        string   `json:"mode"`
	Limitations []string `json:"limitations,omitempty"`
}

type Meta struct {
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DurationMS  int64     `json:"duration_ms"`
	ResultCount int       `json:"result_count"`
}

// AdapterResult 是 Provider binding 与 Core 之间唯一的归一化边界。
type AdapterResult struct {
	Items         []Item            `json:"items"`
	Coverage      []Coverage        `json:"coverage"`
	Errors        []Error           `json:"errors"`
	Limitations   []string          `json:"limitations,omitempty"`
	ProviderState map[string]string `json:"provider_state,omitempty"`
}
