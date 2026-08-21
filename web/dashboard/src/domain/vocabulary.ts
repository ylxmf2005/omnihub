/**
 * Domain vocabulary: enum value -> what a user is told.
 *
 * The Backend speaks in codes (`network_error`, `degraded`, `upstream_retention_unknown`).
 * The product must speak in consequences: what is wrong, and what to do about it.
 * Raw codes stay reachable for support and for tracing UI against API, but they
 * are never the headline a user has to decode.
 *
 * Domain nouns the handoff requires us to keep — Source, Provider, Channel,
 * Endpoint, Egress, View, Run, Probe — are deliberately NOT translated.
 */

import type { ReadinessState, RunStatus, ViewStatus } from '../api/types'

export type Tone = 'ok' | 'conditional' | 'warn' | 'action' | 'bad' | 'idle'

export interface StateCopy {
  /** Shown to the user. */
  label: string
  tone: Tone
  /** One line explaining the consequence, used in detail contexts. */
  meaning: string
}

export const READINESS_COPY: Record<ReadinessState, StateCopy> = {
  ready: { label: '可用', tone: 'ok', meaning: '最近一次 Probe 成功，且结果仍在有效期内。' },
  ready_dependent: {
    label: '部分线路可用',
    tone: 'conditional',
    meaning: '同一 route group 在某些 Egress 下成功、在另一些下失败。',
  },
  degraded: { label: '待确认', tone: 'warn', meaning: '配置完整，但最近一次 Probe 未通过或尚未执行。' },
  needs_login: { label: '待登录', tone: 'action', meaning: '需要你在浏览器中登录该来源。' },
  needs_permission: {
    label: '待授权',
    tone: 'action',
    meaning: '需要你授予 Chrome 对该站点的读取权限。',
  },
  blocked: { label: '被拒绝', tone: 'bad', meaning: '上游明确拒绝了请求。' },
  not_configured: { label: '未配置', tone: 'idle', meaning: '缺少必需的 Egress 或 Endpoint 绑定。' },
  unknown: { label: '未知', tone: 'idle', meaning: '暂时没有足以判断的依据。' },
}

export const VIEW_COPY: Record<ViewStatus, StateCopy> = {
  fresh: { label: '最新', tone: 'ok', meaning: 'Snapshot 仍在有效期内。' },
  stale: { label: '已过期', tone: 'warn', meaning: '内容仍可读取，但已超过新鲜期。' },
  refreshing: { label: '刷新中', tone: 'conditional', meaning: '正在后台刷新，旧内容仍可读。' },
  empty: { label: '暂无内容', tone: 'idle', meaning: '尚未生成任何 Snapshot。' },
  failed: { label: '刷新失败', tone: 'bad', meaning: '刷新失败，且没有可用的历史 Snapshot。' },
}

export const RUN_COPY: Record<RunStatus, StateCopy> = {
  queued: { label: '排队中', tone: 'idle', meaning: '已创建，尚未开始执行。' },
  running: { label: '执行中', tone: 'conditional', meaning: '正在执行。' },
  complete: { label: '成功', tone: 'ok', meaning: '选中的线路都已成功完成。' },
  partial: { label: '部分完成', tone: 'warn', meaning: '取到了结果，但覆盖范围不完整。' },
  failed: { label: '失败', tone: 'bad', meaning: '没有任何线路完成。' },
  cancelled: { label: '已取消', tone: 'idle', meaning: '执行已被取消。' },
}

/**
 * Error codes as a user-facing sentence plus the action that resolves it.
 * `network_error — request upstream feed` is a log line, not a product message.
 */
export const ERROR_COPY: Record<string, { text: string; hint?: string }> = {
  network_error: {
    text: '连接不上这个来源',
    hint: '可能是对方暂时不可达，或当前网络与 Egress 到不了它。可以稍后重试。',
  },
  timeout: { text: '来源响应超时', hint: '对方响应过慢。可以稍后重试。' },
  auth_error: { text: '凭据被拒绝', hint: '请检查该 Channel 使用的 Credential 是否有效。' },
  rate_limited: { text: '被对方限流', hint: '请求过于频繁，请稍后再试。' },
  upstream_error: { text: '来源返回了错误', hint: '对方服务出错，通常需要等待其恢复。' },
  parse_error: { text: '内容无法解析', hint: '返回内容不是有效的 Feed 格式。' },
  protocol_error: { text: '响应不符合预期格式' },
  config_error: { text: '配置不完整', hint: '请补齐该 Channel 的 Egress 或 Endpoint 绑定。' },
  parameter_error: { text: '请求参数无效' },
  internal_error: { text: 'OmniHub 内部错误' },
  browser_unavailable: {
    text: 'Chrome Bridge 未连接',
    hint: '依赖 Cookie 的 Channel 暂时无法执行，已保存的内容仍可读取。',
  },
  browser_permission_missing: {
    text: '缺少 Chrome 站点授权',
    hint: '需要你授予该站点的读取权限。',
  },
  cookie_missing: { text: '未取到登录 Cookie', hint: '可能需要重新登录该站点。' },
  similarity_unavailable: { text: 'Semantic 分组不可用', hint: 'embedding Endpoint 当前不可达。' },
}

export function describeError(code: string, retryable?: boolean): { text: string; hint?: string } {
  const known = ERROR_COPY[code]
  if (known) return known
  // Unknown code: say what we actually know instead of printing the raw token.
  return {
    text: '出现一个未预期的错误',
    hint: retryable ? '该错误可以重试。' : undefined,
  }
}

/**
 * RFC 9457 problem codes, taken from the `writeProblemCode` call sites in
 * `internal/transport`. These are pre-execution refusals — the request never
 * reached an upstream — so the copy is about what the user must change, not
 * about a remote failure.
 */
export const PROBLEM_COPY: Record<string, { text: string; hint?: string }> = {
  revision_conflict: {
    text: '这条记录已经被改动过',
    hint: '你读到它之后它又变了。请重新读取当前内容，确认后再提交，避免覆盖掉别人的修改。',
  },
  resource_in_use: {
    text: '仍被其他配置引用，无法删除',
    hint: '先解除引用它的 Channel 或 View，再删除。',
  },
  resource_not_found: { text: '这条记录已不存在', hint: '它可能已被删除，请刷新列表。' },
  resource_id_mismatch: { text: '请求里的 ID 与目标不一致' },
  if_match_required: { text: '缺少并发校验信息', hint: '请重新读取后再提交。' },
  invalid_if_match: { text: '并发校验信息无效', hint: '请重新读取后再提交。' },
  body_revision_forbidden: { text: '请求体不应携带 revision', hint: 'revision 由并发校验头传递。' },
  action_body_forbidden: { text: '该操作不接受请求体' },
  idempotency_key_required: { text: '该操作需要幂等键' },
  idempotency_conflict: {
    text: '相同的幂等键已用于另一个请求',
    hint: '换一次新的尝试，或查看已存在的那次执行结果。',
  },
  run_state_conflict: { text: 'Run 当前状态不允许这个操作' },
  probe_not_configured: {
    text: '这个 Channel 不支持连通性 Probe',
    hint: '只有 Feed 与 RSSHub 类型实现了分层 Probe，其余类型无法探测。',
  },
  // Returned by the Chrome authorization descriptor route when the Channel does
  // not use Cookie auth at all, so it reads as a mismatch rather than a failure.
  scope_invalid: {
    text: '这个 Channel 不使用 Chrome Cookie',
    hint: '只有绑定 Cookie 凭据的 Channel 才有 Chrome 授权范围。',
  },
  snapshot_unavailable: { text: '还没有可读取的 Snapshot', hint: '先刷新一次这个 View。' },
  view_disabled: { text: '这个 View 已停用', hint: '启用后才能刷新或对外分发。' },
  feed_not_found: { text: '找不到这个 Feed' },
  service_not_configured: { text: '这项功能尚未配置' },
  invalid_json: { text: '请求内容不是合法 JSON' },
  invalid_body: { text: '请求内容不符合要求' },
  invalid_request: { text: '请求无效' },
  untrusted_request: {
    text: '请求来源不被信任',
    hint: 'Backend 只接受 loopback 且 Host 与 Origin 匹配的请求。开发模式下需要正确配置代理。',
  },
  unsupported_media_type: { text: '不支持这种内容类型' },
  payload_too_large: { text: '请求内容过大' },
  method_not_allowed: { text: '该地址不支持这个操作' },
  // A 409 on a query endpoint is routing, not concurrency: it means no configured
  // Channel can satisfy the request — typically the chosen Operation needs a
  // capability none of the scoped Channels declares.
  execution_conflict: {
    text: '没有可执行这个请求的 Channel',
    hint: '所选范围内没有 Channel 声明了这个 Operation 需要的能力。换一个 Operation，或为这个来源配置合适的 RouteTemplate。',
  },
  endpoint_not_found: { text: '这个接口不存在' },
  cors_preflight_rejected: { text: '跨域预检被拒绝' },
  internal_error: { text: 'OmniHub 内部错误', hint: '这不是你的配置问题，请查看 Backend 日志。' },
}

export function describeProblem(code: string): { text: string; hint?: string } {
  return PROBLEM_COPY[code] ?? { text: '这个操作没有成功' }
}

/**
 * Coverage limitation codes, phrased as scope caveats rather than failures. These
 * describe what a result could not cover, which is a property of the upstream —
 * not something the user did wrong and not something a retry fixes.
 *
 * The full set carried by the live RouteTemplate catalog is covered here; an
 * unknown code should be shown raw rather than guessed at.
 */
export const LIMITATION_COPY: Record<string, string> = {
  upstream_retention_unknown: '来源未说明保留多久的历史内容',
  local_feed_window_only: '仅在当前 Feed 窗口内搜索',
  web_index_coverage_unknown: '网页索引的覆盖范围未知',
  candidate_results_only: '返回的是候选结果，不保证完整',
  tavily_max_20: '这个来源单次最多返回 20 条',
  x_recent_search_window: '只能搜索近期内容，历史范围有限',
  xurl_shortcut_no_continuation: '这条捷径不支持继续翻页',
  github_repository_metadata_only: '只返回仓库元数据，不含正文',
  rsshub_route_metadata_version_dependent: '字段随 RSSHub 路由版本变化',
}

/** Probe layer names shown on the diagnostics surface. */
export const PROBE_LAYER_COPY: Record<string, string> = {
  dns: '域名解析',
  tcp: '建立连接',
  proxy_connect: '代理连接',
  tls: 'TLS 握手',
  http: 'HTTP 请求',
  feed_parse: 'Feed 解析',
}

/**
 * Readiness check kinds, in the order the Backend evaluates them — a static
 * configuration ladder first, then the one check that actually touches the
 * network.
 *
 * This list is the complete set emitted by `addCheck` in
 * `internal/readiness/readiness.go`. An earlier version of this map contained
 * `credential_present`, which the Backend never sends (the real kind is
 * `credential_resolved`) and which therefore silently rendered as a raw token.
 * Anything not in this list is a contract change, not a naming preference.
 */
export const CHECK_KIND_COPY: Record<string, string> = {
  channel_configured: 'Channel 配置完整',
  source_registered: 'Source 已注册',
  template_declared: 'RouteTemplate 已声明',
  template_trusted: 'RouteTemplate 可信',
  provider_enabled: 'Provider 已启用',
  endpoint_configured: 'Endpoint 已绑定',
  egress_configured: 'Egress 已绑定',
  egress_credential_resolved: 'Egress 凭据可用',
  credential_resolved: 'Credential 可用',
  dependency_installed: '依赖已安装',
  channel_probe: '实际连通性 Probe',
}

/**
 * Check-level codes. A separate namespace from the HTTP problem codes above:
 * these explain why one rung of the readiness ladder did not pass, and they only
 * appear on a check whose status is `failed` or `unknown`.
 *
 * The distinction that matters most to a reader is the last two entries.
 * `upstream_not_probed` means "this line can be probed, but there is no valid
 * result right now" — a probe was never run, or its result passed its TTL and was
 * dropped. `probe_unsupported` means "this line can never be probed", because v1
 * only implements layered Probe for Feed and RSSHub. Presenting the second as if
 * it were the first would invite a user to keep pressing a button that cannot
 * work.
 */
export const CHECK_CODE_COPY: Record<string, { text: string; hint?: string }> = {
  channel_not_configured: {
    text: 'Channel 配置不完整',
    hint: '缺少必需的参数或绑定，请补齐后再检查。',
  },
  disabled_by_user: { text: '已被手动停用', hint: '启用后才会参与查询。' },
  source_unavailable: { text: 'Source 不可用', hint: '它可能已被停用或不再注册。' },
  template_missing: { text: 'RouteTemplate 不存在', hint: '它引用的模板已不在 Catalog 中。' },
  template_disabled: { text: 'RouteTemplate 已停用' },
  template_untrusted: {
    text: 'RouteTemplate 不被信任',
    hint: '当前信任级别不允许用它取数。',
  },
  provider_unavailable: { text: 'Provider 不可用' },
  endpoint_missing: { text: '缺少 Endpoint 绑定', hint: '这个模板必须绑定一个 Endpoint。' },
  endpoint_disabled: { text: 'Endpoint 已停用' },
  credential_missing: { text: '缺少 Credential', hint: '这条线路需要凭据才能访问上游。' },
  credential_unresolved: {
    text: 'Credential 无法使用',
    hint: '它可能已被撤销，或引用了不存在的记录。',
  },
  dependency_unavailable: { text: '依赖不可用', hint: '所需的外部命令或组件当前不可用。' },
  dependency_not_probed: { text: '依赖尚未检查', hint: '依赖状态未知，因此无法判定可用性。' },
  probe_failed: {
    text: 'Probe 未通过',
    hint: '实际连接上游时失败了，且判定为持续性问题。详情见下方分层结果。',
  },
  // probe_failed escalates the Channel to `blocked`; probe_degraded only to
  // `degraded`, and fires when the report is degraded or the failure was marked
  // transient. Merging the two would lose exactly the retry-or-give-up signal.
  probe_degraded: {
    text: 'Probe 结果不理想',
    hint: '这次失败被判定为暂时性的，稍后重新检查可能就能通过。',
  },
  // The egress family is assembled by trimming a `preflight_` prefix, so these
  // codes have no literal in the source and are easy to miss.
  egress_conflict: {
    text: 'Egress 绑定冲突',
    hint: 'Endpoint 已经固定了出口，Channel 不能再绑定另一个。',
  },
  egress_missing: { text: '缺少 Egress 绑定', hint: '这条线路必须指定出站方式。' },
  egress_not_found: {
    text: 'Egress 不存在',
    hint: '它引用的 Egress 已被删除，请重新绑定一个。',
  },
  egress_disabled: { text: 'Egress 已停用', hint: '启用它，或为这条线路换一个出口。' },
  egress_credential_missing: {
    text: 'Egress 缺少凭据',
    hint: '这个出口（例如需要认证的代理）还没有绑定 Credential。',
  },
  egress_credential_unresolved: {
    text: 'Egress 凭据无法使用',
    hint: '它可能已被撤销，或引用了不存在的记录。',
  },
  upstream_not_probed: {
    text: '尚无有效的 Probe 结果',
    hint: '可能从未检查过，也可能上次结果已过期。过期本身不是失败，重新检查一次即可确认。',
  },
  probe_unsupported: {
    text: '这个类型无法 Probe',
    hint: '只有 Feed 与 RSSHub 实现了分层 Probe，其余类型的可用性只能由实际查询体现。',
  },
}

export function describeCheckCode(code: string | undefined): { text: string; hint?: string } | null {
  if (!code) return null
  return CHECK_CODE_COPY[code] ?? { text: '这一项没有通过' }
}
