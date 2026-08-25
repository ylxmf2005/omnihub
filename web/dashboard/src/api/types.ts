/**
 * Backend contract types.
 *
 * Every field here was read off a live `omnihub serve` (GET /openapi.json plus
 * the actual response bodies) rather than transcribed from prose, because the
 * two disagreed in several places: `/v1/latest` rejects an `operation` field
 * that `POST /v1/views` requires, `Channel` carries `source` and `parameters`
 * rather than a `source_id`, and `ViewListEntry.snapshot` embeds a base64
 * Envelope the list pages must not try to decode.
 *
 * When this file and the API disagree, the API is right.
 */

// ---------------------------------------------------------------------------
// Enums. Closed sets — a value outside them is a contract change, not a string.
// ---------------------------------------------------------------------------

export const READINESS_STATES = [
  'ready',
  'ready_dependent',
  'degraded',
  'needs_login',
  'needs_permission',
  'blocked',
  'not_configured',
  'unknown',
] as const
export type ReadinessState = (typeof READINESS_STATES)[number]

export const VIEW_STATUSES = ['fresh', 'stale', 'refreshing', 'empty', 'failed'] as const
export type ViewStatus = (typeof VIEW_STATUSES)[number]

export const RUN_STATUSES = [
  'queued',
  'running',
  'complete',
  'partial',
  'failed',
  'cancelled',
] as const
export type RunStatus = (typeof RUN_STATUSES)[number]

/** Polling stops here. A Run in any other status is still moving. */
export const TERMINAL_RUN_STATUSES: readonly RunStatus[] = [
  'complete',
  'partial',
  'failed',
  'cancelled',
]

export function isTerminalRun(status: RunStatus): boolean {
  return TERMINAL_RUN_STATUSES.includes(status)
}

export type CheckStatus = 'passed' | 'failed' | 'unknown'

export const ERROR_CODES = [
  'parameter_error',
  'config_error',
  'auth_error',
  'rate_limited',
  'timeout',
  'network_error',
  'upstream_error',
  'protocol_error',
  'parse_error',
  'internal_error',
  'browser_unavailable',
  'browser_permission_missing',
  'cookie_missing',
  'similarity_unavailable',
] as const
export type ErrorCode = (typeof ERROR_CODES)[number]

export interface OmniError {
  code: ErrorCode
  message: string
  retryable: boolean
  source?: string
  provider?: string
  channel_id?: string
  route_template_id?: string
  retry_after_ms?: number | null
  details?: Record<string, unknown>
}

/** RFC 9457 application/problem+json. Pre-execution failures only. */
export interface Problem {
  type: string
  title: string
  status: number
  code: string
  detail?: string
}

// ---------------------------------------------------------------------------
// Operation — the saved query. Shared by View bodies and the query endpoints,
// but NOT identically: see SearchInput / LatestInput / FetchInput below.
// ---------------------------------------------------------------------------

export type OperationKind = 'search' | 'latest' | 'fetch'

/**
 * A route selector is an object, not an id string. Sending a bare string fails
 * with `cannot unmarshal string into Go struct field RoutePolicy...`.
 */
export interface RouteSelector {
  kind: 'channel' | 'provider'
  id: string
}

export interface RoutePolicy {
  mode: 'auto' | 'prefer' | 'only' | 'exclude'
  aggregate: boolean
  allow_fallback: boolean
  prefer?: RouteSelector[] | null
  only?: RouteSelector[] | null
  exclude?: RouteSelector[] | null
}

/** `collection` is singular in the live schema; there is no `collections`. */
export interface OperationScope {
  channels?: string[]
  sources?: string[]
  collection?: string
  providers?: string[]
  domains?: string[]
}

export interface TimeRange {
  from?: string
  to?: string
}

export type SearchContentField = 'title' | 'body' | 'first_post'
export type SearchSort = 'relevance' | 'newest'

export interface SearchConstraints {
  time?: { field?: 'published_at'; from?: string; to?: string }
  authors?: string[]
  categories?: string[]
  tags?: string[]
  content_fields?: SearchContentField[]
}

export interface Operation {
  schema_version: '1.0'
  operation: OperationKind
  scope: OperationScope
  route_policy: RoutePolicy
  limit: number
	/** `latest` only. */
  time_range?: TimeRange
  identity_dedupe: 'none' | 'exact'
  similarity_grouping: 'off' | 'semantic'
  semantic_profile_id?: string | null
  deadline_ms: number
  continuation?: string
  /** `search` only. */
  query?: string
	/** `search` only. */
  constraints?: SearchConstraints
	/** `search` only. */
  sort?: SearchSort
  /** `fetch` only. The field is `target`, matching the fetch endpoint. */
  target?: string
}

/**
 * Bodies for the three query endpoints.
 *
 * These are three different shapes, not one shape with optional extras, and all
 * three set `additionalProperties: false` — so a single `Omit<Operation, …>` both
 * over- and under-describes them:
 *   - none may carry `operation`; the endpoint already names it
 *     (`unknown field "operation"`)
 *   - `fetch` takes `target`, not `url`, and **rejects `limit`**, `time_range`,
 *     `identity_dedupe` and `similarity_grouping` outright
 *   - `search` additionally requires `query`
 * Every listed field is required unless marked optional here.
 */
interface QueryInputBase {
  schema_version: '1.0'
  scope: OperationScope
  route_policy: RoutePolicy
  deadline_ms: number
}

interface RankedQueryInput extends QueryInputBase {
  limit: number
  identity_dedupe: 'none' | 'exact'
  similarity_grouping: 'off' | 'semantic'
  /** Required by the schema when `similarity_grouping` is `semantic`. */
  semantic_profile_id?: string | null
  continuation?: string
}

export interface SearchInput extends RankedQueryInput {
  query: string
  constraints: SearchConstraints
  sort: SearchSort
}

export interface LatestInput extends RankedQueryInput {
  time_range: TimeRange
}

export interface FetchInput extends QueryInputBase {
  target: string
}

export type QueryInput = SearchInput | LatestInput | FetchInput

/** Maps an operation kind to the body shape its endpoint accepts. */
export type QueryInputFor<K extends OperationKind> = K extends 'search'
  ? SearchInput
  : K extends 'fetch'
    ? FetchInput
    : LatestInput

// ---------------------------------------------------------------------------
// Envelope — the result of an execution. Same shape from a query endpoint and
// from `run.result`.
// ---------------------------------------------------------------------------

export interface ItemContent {
  role: 'body' | 'summary' | 'snippet' | string
  html?: string
  text?: string
  source_supplied?: boolean
}

export interface ItemAuthor {
  name: string
  url?: string
}

/**
 * Where one Item was actually observed. The audit trail for a result.
 * The timestamp field is `retrieved_at`; there is no `observed_at`.
 */
export interface ItemObservation {
  source: string
  provider: string
  channel_id: string
  route_template_id: string
  endpoint?: string
  upstream_id?: string
  original_url?: string
  canonical_url?: string
  retrieved_at?: string
  /** Position in the upstream's own ordering, where it has one. */
  rank?: number
  verification?: Record<string, unknown> | string
}

export interface Item {
  id: string
  url: string
  title: string
  content?: ItemContent
  published_at?: string
  modified_at?: string
  authors?: ItemAuthor[]
  observations: ItemObservation[]
  identity?: Record<string, unknown>
  similarity?: Record<string, unknown> | null
}

/** How much of a channel was actually covered. `truncated` is why a Run is `partial`. */
export interface CoverageEntry {
  source: string
  channel_id: string
  route_template_id: string
  scope: string
  from?: string
  to?: string
  examined: number
  returned: number
  exhaustive: boolean
  truncated: boolean
  limitations?: string[]
}

/**
 * One channel attempt inside an execution. `egress` records which outbound route
 * was actually used, which is the evidence that the Backend did not silently fall
 * back to a direct connection.
 */
export interface ExecutionRecord {
  channel_id: string
  route_template_id: string
  source: string
  provider: string
  capability: string
  selection: 'primary' | 'fallback' | string
  status: 'completed' | 'failed' | 'skipped' | string
  reason?: string
  started_at: string
  duration_ms: number
  fresh_until?: string
  examined?: number
  returned?: number
  auth?: Record<string, unknown>
  egress?: { profile_id: string; mode: string; proxied: boolean }
  limitations?: string[]
  error?: OmniError
}

export interface Continuation {
  mode: 'none' | 'opaque' | string
  token?: string | null
  limitations: string[]
}

export interface EnvelopeMeta {
  started_at: string
  finished_at: string
  duration_ms: number
  result_count: number
}

export interface Envelope {
  schema_version: string
  request_id: string
  status: 'complete' | 'partial' | 'failed'
  request: Operation
  selected_channel_ids: string[]
  executions: ExecutionRecord[]
  items: Item[]
  coverage: CoverageEntry[]
  errors: OmniError[]
  continuation: Continuation
  meta: EnvelopeMeta
}

// ---------------------------------------------------------------------------
// Readiness
// ---------------------------------------------------------------------------

export interface ReadinessCheck {
  kind: string
  status: CheckStatus
  code?: string
  checked_at: string
  expires_at?: string
  error?: OmniError
}

export interface ActionRequired {
  kind: string
  /** The only trusted source of a login link. Never assembled from a Source name. */
  url?: string
}

export interface ChannelHealth {
  channel_id: string
  desired_state: 'enabled' | 'disabled'
  readiness: ReadinessState
  checks: ReadinessCheck[]
  action_required: ActionRequired | null
  last_successful_probe_at?: string
}

/** `ready_dependent` only: one route group, opposite results per Egress. */
export interface RouteGroupHealth {
  route_group: string
  readiness: ReadinessState
  channel_ids: string[]
  ready_egress_profile_ids: string[]
  failed_egress_profile_ids: string[]
}

/** GET /v1/readiness */
export interface ReadinessReport {
  schema_version: string
  generated_at: string
  channels: ChannelHealth[]
  route_groups: RouteGroupHealth[]
}

// ---------------------------------------------------------------------------
// Registry resources
// ---------------------------------------------------------------------------

/**
 * GET /v1/channels
 *
 * Note the read model differs from the write model: reads return `source` and a
 * `parameters` bag; `POST /v1/channels` takes `source_id`, `channel_id`,
 * `channel_display_name` and a flat `url`.
 */
export interface Channel {
  id: string
  display_name?: string
  source: string
  route_template_id: string
  endpoint_profile_id?: string
  egress_profile_id?: string
  credential_id?: string
  parameters?: Record<string, unknown>
  priority: number
  enabled: boolean
  revision: number
}

/**
 * POST /v1/channels body.
 *
 * Required: `id`, `source_id`, `route_template_id`, `priority`, `enabled`.
 * Note this is NOT the CLI's `channels apply` shape — the CLI takes
 * `channel_id` / `channel_display_name`, the HTTP API takes `id` /
 * `display_name`, and sending the CLI names over HTTP fails as an unknown field.
 */
export interface ChannelInput {
  id: string
  source_id: string
  route_template_id: string
  priority: number
  enabled: boolean
  display_name?: string
  source_display_name?: string
  source_canonical_url?: string
  url?: string
  parameters?: Record<string, unknown>
  egress_profile_id?: string
  endpoint_profile_id?: string
  credential_id?: string
  collection_ids?: string[] | null
  fallback_channel_ids?: string[] | null
}

export interface EgressProfile {
  id: string
  display_name?: string
  mode: 'direct' | 'environment' | 'http_proxy' | 'socks5'
  proxied: boolean
  has_endpoint: boolean
  credential_id?: string
  enabled: boolean
  revision: number
}

/** POST/PUT /v1/egress-profiles. Requires `id`, `mode`, `enabled`. */
export interface EndpointProfile {
  id: string
  provider: string
  base_url?: string
  egress_profile_id?: string
  trust?: string
  options?: Record<string, unknown>
  enabled: boolean
  revision: number
}

/** POST/PUT /v1/endpoint-profiles. Requires `id`, `provider`, `base_url`, `egress_profile_id`. */
/** GET /v1/credentials. Dashboard responses never carry the secret value. */
export interface Credential {
  id: string
  provider: string
  auth_kind: string
  label?: string
  has_value: boolean
  value_masked?: string
  enabled: boolean
  revision: number
}

/** POST /v1/credentials. Requires `id`, `provider`, `auth_kind`, `value`, `enabled`. */
export interface CredentialInput {
  id: string
  provider: string
  auth_kind: string
  value: string
  enabled: boolean
  label?: string
}

/**
 * A Semantic Profile is what `similarity_grouping: "semantic"` resolves to. It
 * binds an embedding Endpoint plus the vector parameters; `index_revision` exists
 * so changing the model or dimension invalidates previously grouped results
 * rather than silently mixing incompatible vectors.
 */
/**
 * GET /v1/sources. Deliberately thin — the catalog is reference material.
 *
 * `display_name` is present on only some Sources (live: `tavily-discovery` has
 * one, the other five don't), so callers must fall back to `id`.
 */
export interface Source {
  id: string
  origin: 'builtin' | 'user' | string
  enabled: boolean
  display_name?: string
}

/**
 * GET /v1/route-templates
 *
 * Two fields here were verified against the live catalog rather than the spec,
 * because the spec's shape does not hold:
 *   - the constrained Source list is `source_constraint.values`, not `sources`
 *   - `parameters_schema` is genuinely absent on some templates (live:
 *     `github-native-search`, `x-xurl-search`), so it cannot be required
 * `content_level` also carries values beyond the obvious three (`snippet` on
 * `tavily-search`), which is why the union stays open.
 */
export interface RouteTemplate {
  route_template_id: string
  origin: 'builtin' | 'user' | string
  source_constraint: { kind: string; values?: string[] }
  provider: string
  adapter: string
  capabilities: OperationKind[]
  content_level: 'body' | 'summary' | 'metadata' | 'snippet' | string
  pagination: { kind: string; globally_mergeable: boolean }
  time_range: { kind: string }
  search_constraints: {
    time?: { mode?: string; field?: string; precision?: string }
    authors?: { mode?: string }
    categories?: { mode?: string }
    tags?: { mode?: string }
    content_fields?: { mode?: string }
    sorts?: SearchSort[]
  }
  auth: { kind: string; required: boolean }
  /** Absent on templates whose parameters are fixed by their Provider. */
  parameters_schema?: Record<string, unknown>
  /** Present and true only on templates that must be bound to an Endpoint. */
  endpoint_required?: boolean
  cost: 'free' | 'paid' | string
  trust: string
  limitations: string[]
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

export interface RunProgress {
  channels_total: number
  channels_finished: number
}

/** GET /v1/runs, GET /v1/runs/{id} */
export interface Run {
  id: string
  kind: 'query' | 'view_refresh' | 'channel_probe'
  resource: { type: string; id: string }
  request_id: string
  request?: Operation
  idempotency_key: string
  status: RunStatus
  claimed_by?: string
  attempt: number
  progress: RunProgress
  revision: number
  result?: Envelope
  last_error?: OmniError
  created_at: string
  started_at?: string
  finished_at?: string
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

export interface View {
  id: string
  display_name: string
  operation: Operation
  enabled: boolean
  revision: number
  created_at: string
  updated_at: string
}

/**
 * `envelope` is a base64 blob of the whole materialized result and `state_keys`
 * is internal bookkeeping. List pages must not decode either; read items from
 * `GET /v1/views/{id}/items` instead.
 */
export interface SnapshotMeta {
  id: string
  view_id: string
  run_id: string
  state_keys?: string[]
  envelope?: string
  created_at: string
  fresh_until: string
}

/**
 * GET /v1/views. Feed URLs come from the API and are never assembled here.
 *
 * `last_failure` is the failed **Run**, not an error: the error itself is at
 * `last_failure.last_error`. Passing the Run to `describeError` yields "出现一个
 * 未预期的错误" for every real failure, because a Run has no `code` field.
 */
export interface ViewListEntry {
  view: View
  status: ViewStatus
  feed_urls: { json: string; rss: string; atom: string }
  snapshot?: SnapshotMeta
  active_run?: Run | null
  last_failure?: Run | null
}

/**
 * GET /v1/views/{id} returns the same wrapper as the list entry, not a bare
 * `View`. Typing it as `View` makes every field read `undefined` one level too
 * shallow.
 */
export type ViewDetailResponse = ViewListEntry

export interface ViewInput {
  id: string
  display_name: string
  operation: Operation
  enabled: boolean
  expected_revision?: number
}

/** GET /v1/views/{id}/items */
export interface ViewItems {
  snapshot_id: string
  view_id: string
  stale: boolean
  items: Item[]
}

// ---------------------------------------------------------------------------
// Browser bridge
// ---------------------------------------------------------------------------

/** GET /v1/browser-bridges. A single object, not a list. */
export interface BrowserBridge {
  id: string
  browser: string
  connected: boolean
  profile_label: string
  granted_origins?: string[] | null
  last_seen_at: string
  last_error?: OmniError | null
}

/** GET /v1/channels/{id}/chrome/authorization-descriptor. Not a grant. */
export interface AuthorizationDescriptor {
  login_url: string
  permission_origin_pattern: string
  cookie_scope: {
    url: string
    allowed_domains: string[]
    names: string[]
    store: string
    partitions: string[]
  }
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

/** Every mutable resource carries a revision; the ETag mirrors it. */
export interface Revisioned {
  revision: number
}
