/**
 * Channel detail — the diagnostic depth the Overview page deliberately omits.
 *
 * Four things here were decided against the live Backend rather than the spec,
 * because the two disagree:
 *
 *   **Readiness is layered evidence, not a boolean.** `checks[]` arrives in
 *   dependency order: the static facts (Source registered, RouteTemplate
 *   declared, Egress bound) must pass before the last layer — a real network
 *   Probe — is even attempted. So the ladder is rendered in the Backend's own
 *   order, which makes "how far did it get" readable without any commentary.
 *
 *   **A Probe result expires, and the Backend does not keep the expired one.**
 *   `internal/readiness` only projects probe records that are still valid, so a
 *   lapsed Probe is *indistinguishable* from one that never ran: both come back
 *   as `channel_probe` / `unknown` / `upstream_not_probed`, and both drop
 *   `expires_at` and `last_successful_probe_at` entirely. This page therefore
 *   never claims "passed earlier, then expired" from readiness alone. It shows
 *   the validity window while a Probe is live, and otherwise cites the actual
 *   probe Runs from `/v1/runs` — real recorded history rather than an inference.
 *
 *   **"Not verified" and "not probeable" are different facts.** Only Feed and
 *   RSSHub adapters implement the layered Probe; GitHub, Tavily and xurl report
 *   `probe_unsupported`. For those the Probe action is withheld, because
 *   offering a button that cannot work reads as "nobody has run it yet".
 *
 *   **The write model is not the read model, and PUT is a full replacement.**
 *   Reads return `source` and a `parameters` bag; the write side demands
 *   `source_id` and a flat `url`, and rejects `parameters` outright — verified
 *   against the live contract, which `openapi.json` contradicts by listing
 *   `parameters` as writable. Anything in `parameters` besides `url` therefore
 *   cannot survive a round-trip, so editing such a Channel is refused instead of
 *   silently discarding its configuration.
 */

import { useEffect, useMemo, useState, type ReactNode } from 'react'
import {
  Anchor,
  Badge,
  Box,
  Button,
  Group,
  Modal,
  NumberInput,
  Select,
  Stack,
  Switch,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPencil, IconRefresh, IconStethoscope, IconTrash } from '@tabler/icons-react'
import { useParams } from 'react-router'
import {
  keys,
  READ_ONLY,
  useChannel,
  useChannels,
  useChromeDescriptor,
  useDelete,
  useEgressProfiles,
  useProbeChannel,
  useReadiness,
  useRouteTemplates,
  useRun,
  useRuns,
  useUpdate,
} from '../api/queries'
import { get } from '../api/client'
import type {
  Channel,
  ChannelHealth,
  ChannelInput,
  CheckStatus,
  ReadinessCheck,
  Run,
} from '../api/types'
import { isTerminalRun } from '../api/types'
import { IdChip, ReadinessBadge, RunBadge, StateBadge, Timestamp } from '../components/display'
import {
  CardRow,
  Caveat,
  ConfirmDialog,
  DemoModeNotice,
  EmptyState,
  ErrorAlert,
  Fact,
  FieldList,
  JsonBlock,
  LoadingRow,
  MetaLine,
  PageHeader,
  PageLink,
  SectionCard,
} from '../components/layout'
import {
  CHECK_KIND_COPY,
  READINESS_COPY,
  describeCheckCode,
  describeError,
  type Tone,
} from '../domain/vocabulary'

// ---------------------------------------------------------------------------
// Page-local vocabulary
// ---------------------------------------------------------------------------

const CHECK_STATUS_COPY: Record<CheckStatus, { label: string; tone: Tone; meaning: string }> = {
  passed: { label: '通过', tone: 'ok', meaning: '这一层已经验证通过。' },
  failed: { label: '未通过', tone: 'bad', meaning: '这一层明确失败，是当前不可用的直接原因。' },
  unknown: {
    label: '未验证',
    tone: 'idle',
    meaning: '这一层没有得出结论，既不算通过也不算失败。',
  },
}

const ACTION_KIND_COPY: Record<string, string> = {
  grant_browser_permission: '需要你在 Chrome 扩展里授予这个站点的读取权限。',
  install_dependency: '需要先安装这个 Adapter 依赖的命令行工具。',
  review_template: '需要你确认是否信任这个 RouteTemplate。',
  start_chrome_bridge: '需要先让 Chrome 扩展连上 OmniHub。',
}

// ---------------------------------------------------------------------------
// Probe validity
// ---------------------------------------------------------------------------

interface ProbeValidity {
  expired: boolean
  minutesLeft: number
}

/** A Probe result is time-boxed; `expires_at` is the only source of that window. */
function probeValidity(check: ReadinessCheck | undefined): ProbeValidity | null {
  if (!check?.expires_at) return null
  const expires = new Date(check.expires_at).getTime()
  if (Number.isNaN(expires)) return null
  const remaining = expires - Date.now()
  return { expired: remaining <= 0, minutesLeft: Math.max(0, Math.round(remaining / 60_000)) }
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ChannelDetail() {
  const { id } = useParams<{ id: string }>()
  const detail = useChannel(id)
  const list = useChannels()
  const readiness = useReadiness()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  // The detail route carries the ETag and is authoritative, but it is disabled
  // in demo mode; the list fixture still describes the Channel, so the page
  // stays readable there even though it cannot write.
  const channel: Channel | undefined =
    detail.data?.data ?? (list.data ?? []).find((entry) => entry.id === id)
  const etag = detail.data?.etag ?? null

  const health: ChannelHealth | undefined = (readiness.data?.channels ?? []).find(
    (entry) => entry.channel_id === id,
  )
  const probeCheck = health?.checks.find((check) => check.kind === 'channel_probe')
  const probeable = probeCheck?.code !== 'probe_unsupported'

  if (detail.isLoading && !channel) {
    return (
      <Stack gap="lg">
        <PageHeader title="Channel" />
        <LoadingRow />
      </Stack>
    )
  }

  if (!channel) {
    return (
      <Stack gap="lg">
        <PageHeader title="Channel" />
        {detail.error ? (
          <ErrorAlert error={detail.error} onRetry={() => void detail.refetch()} />
        ) : (
          <SectionCard title="找不到这条 Channel">
            <EmptyState
              title="这个 ID 在当前配置里不存在。"
              hint="它可能已经被删除，或者地址里的 ID 有误。"
              action={<PageLink to="/channels">返回 Channels 列表</PageLink>}
            />
          </SectionCard>
        )}
      </Stack>
    )
  }

  return (
    <Stack gap="lg">
      <PageHeader
        title={channel.display_name ?? channel.id}
        description={
          <MetaLine gap="md">
            <Fact label="Source">{channel.source}</Fact>
            <Fact label="RouteTemplate">{channel.route_template_id}</Fact>
          </MetaLine>
        }
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              loading={detail.isFetching || readiness.isFetching}
              onClick={() => {
                void detail.refetch()
                void readiness.refetch()
              }}
            >
              刷新
            </Button>
            <Button
              variant="default"
              leftSection={<IconPencil size={14} />}
              disabled={READ_ONLY}
              onClick={() => setEditing(true)}
            >
              编辑
            </Button>
            <Button
              variant="subtle"
              color="red"
              leftSection={<IconTrash size={14} />}
              disabled={READ_ONLY}
              onClick={() => setDeleting(true)}
            >
              删除
            </Button>
          </>
        }
      />

      <MetaLine gap="md">
        <PageLink to="/channels">返回 Channels 列表</PageLink>
        <PageLink to="/diagnostics">在 Diagnostics 中查看全部线路</PageLink>
      </MetaLine>

      <DemoModeNotice />
      {detail.error ? (
        <ErrorAlert error={detail.error} onRetry={() => void detail.refetch()} />
      ) : null}

      <ReadinessPanel channel={channel} health={health} probeCheck={probeCheck} />

      <CheckLadder health={health} loading={readiness.isLoading} />

      <ProbePanel channelId={channel.id} probeable={probeable} probeCheck={probeCheck} />

      <ConfigPanel channel={channel} />

      <ChromePanel channel={channel} />

      <EditChannelModal
        channel={channel}
        etag={etag}
        opened={editing}
        onClose={() => setEditing(false)}
      />
      <DeleteChannelDialog channel={channel} opened={deleting} onClose={() => setDeleting(false)} />
    </Stack>
  )
}

// ---------------------------------------------------------------------------
// Readiness summary
// ---------------------------------------------------------------------------

function ReadinessPanel({
  channel,
  health,
  probeCheck,
}: {
  channel: Channel
  health?: ChannelHealth
  probeCheck?: ReadinessCheck
}) {
  const validity = probeValidity(probeCheck)

  return (
    <SectionCard title="当前就绪度">
      <CardRow>
        {health ? (
          <Stack gap="sm">
            <Group gap="xs" wrap="wrap">
              <ReadinessBadge state={health.readiness} />
              {!channel.enabled && (
                <Badge variant="default" size="xs" styles={{ label: { fontWeight: 400 } }}>
                  已停用
                </Badge>
              )}
              {health.desired_state === 'disabled' && (
                <Badge variant="default" size="xs" styles={{ label: { fontWeight: 400 } }}>
                  期望状态为停用
                </Badge>
              )}
            </Group>

            <Text fz="sm">{READINESS_COPY[health.readiness].meaning}</Text>

            {/* The TTL is the whole reason a passing Channel drifts back to
                待确认 without anything breaking, so it is stated, not implied. */}
            {probeCheck?.status === 'passed' && validity && !validity.expired && (
              <Text fz="xs" c="dimmed">
                这次 Probe 的结论有效期还剩约 {validity.minutesLeft} 分钟。过期后这条 Channel 会自动
                回到「待确认」，Probe 检查项也会变回「未验证」——那不是故障，只是结论过期了。
              </Text>
            )}

            {health.last_successful_probe_at && (
              <MetaLine gap="md">
                <Fact label="上次成功 Probe">
                  <Timestamp iso={health.last_successful_probe_at} relative />
                </Fact>
                {probeCheck?.expires_at && (
                  <Fact label="结论有效至">
                    <Timestamp iso={probeCheck.expires_at} />
                  </Fact>
                )}
              </MetaLine>
            )}

            {health.action_required && (
              <Caveat>
                {ACTION_KIND_COPY[health.action_required.kind] ?? '需要你先完成一步手动操作。'}
                {health.action_required.url && (
                  <>
                    {' '}
                    <Anchor
                      href={health.action_required.url}
                      target="_blank"
                      rel="noreferrer"
                      fz="xs"
                    >
                      打开该站点
                    </Anchor>
                  </>
                )}
              </Caveat>
            )}
          </Stack>
        ) : (
          <Text fz="sm" c="dimmed">
            readiness 里没有这条 Channel 的健康记录。
          </Text>
        )}
      </CardRow>
    </SectionCard>
  )
}

// ---------------------------------------------------------------------------
// The ladder
// ---------------------------------------------------------------------------

function CheckLadder({ health, loading }: { health?: ChannelHealth; loading: boolean }) {
  return (
    <SectionCard
      title="分层检查证据"
      count={health ? `${health.checks.length} 层` : undefined}
      action={
        <Text fz="xs" c="dimmed">
          按 Backend 的判定顺序排列
        </Text>
      }
    >
      {loading ? (
        <LoadingRow />
      ) : !health || health.checks.length === 0 ? (
        <EmptyState
          title="没有可展示的检查记录。"
          hint="readiness 只对已注册的 Channel 给出分层结论。"
        />
      ) : (
        health.checks.map((check, index) => (
          <CheckRow key={`${check.kind}-${index}`} index={index + 1} check={check} />
        ))
      )}
    </SectionCard>
  )
}

function CheckRow({ index, check }: { index: number; check: ReadinessCheck }) {
  const status = CHECK_STATUS_COPY[check.status]
  const codeCopy = describeCheckCode(check.code)
  const failure = check.error ? describeError(check.error.code, check.error.retryable) : undefined
  const validity = probeValidity(check)

  return (
    <CardRow>
      <Group align="flex-start" wrap="nowrap" gap="md">
        <Text fz="xs" c="dimmed" ff="monospace" style={{ width: 20, flex: 'none' }}>
          {index}
        </Text>
        <Box style={{ minWidth: 0, flex: '1 1 auto' }}>
          <Group gap="xs" wrap="wrap">
            <Text fz="sm" fw={500}>
              {CHECK_KIND_COPY[check.kind] ?? '附加检查'}
            </Text>
            <StateBadge
              label={status.label}
              tone={status.tone}
              code={check.status}
              meaning={status.meaning}
            />
          </Group>

          {codeCopy && (
            <Box mt={4}>
              <Text fz="xs">{codeCopy.text}</Text>
              {codeCopy.hint && (
                <Text fz="xs" c="dimmed" mt={2}>
                  {codeCopy.hint}
                </Text>
              )}
            </Box>
          )}

          {failure && (
            <Box mt={4}>
              <Text fz="xs">{failure.text}</Text>
              {failure.hint && (
                <Text fz="xs" c="dimmed" mt={2}>
                  {failure.hint}
                </Text>
              )}
            </Box>
          )}

          <MetaLine gap="md">
            <Fact label="判定于">
              <Timestamp iso={check.checked_at} relative />
            </Fact>
            {check.expires_at && (
              <Fact label={validity?.expired ? '已过期于' : '有效至'}>
                <Timestamp iso={check.expires_at} />
              </Fact>
            )}
          </MetaLine>

          {/* Raw codes are traceability, not the message: dimmed, monospace, last. */}
          {(check.code || check.error) && (
            <Text fz={10} c="dimmed" ff="monospace" mt={4}>
              {[check.kind, check.code, check.error?.code].filter(Boolean).join(' ')}
            </Text>
          )}
        </Box>
      </Group>
    </CardRow>
  )
}

// ---------------------------------------------------------------------------
// Probe
// ---------------------------------------------------------------------------

function ProbePanel({
  channelId,
  probeable,
  probeCheck,
}: {
  channelId: string
  probeable: boolean
  probeCheck?: ReadinessCheck
}) {
  const probe = useProbeChannel()
  const readiness = useReadiness()
  const runs = useRuns(20)
  const activeRun = useRun(probe.data?.id, Boolean(probe.data))

  // Readiness is derived from probe records, so it only tells the truth again
  // once the Run has actually settled.
  const status = activeRun.data?.status
  useEffect(() => {
    if (status && isTerminalRun(status)) {
      void readiness.refetch()
      void runs.refetch()
    }
  }, [status])

  const failure = activeRun.data?.last_error
    ? describeError(activeRun.data.last_error.code, activeRun.data.last_error.retryable)
    : undefined

  const history = (runs.data ?? []).filter(
    (run) => run.kind === 'channel_probe' && run.resource.id === channelId,
  )

  return (
    <SectionCard
      title="连通性 Probe"
      action={
        <Button
          size="compact-sm"
          variant="default"
          leftSection={<IconStethoscope size={13} />}
          disabled={READ_ONLY || !probeable}
          loading={probe.isPending}
          onClick={() => probe.mutate(channelId)}
        >
          执行 Probe
        </Button>
      }
    >
      <CardRow>
        <Stack gap="sm">
          {probeable ? (
            <Text fz="sm">
              Probe 会按 域名解析、建立连接、TLS 握手、HTTP 请求、Feed 解析 逐层实际发起请求。
              只有它成功，这条 Channel 才会变成「可用」。结论带有效期，过期后需要重新执行。
            </Text>
          ) : (
            <Caveat>
              这个 Channel 的 RouteTemplate 没有实现分层 Probe，只有 Feed 与 RSSHub 类型实现了它。
              所以它的 Probe 检查项会一直停在「未验证」——这不是「还没探测」，而是无法探测。
            </Caveat>
          )}

          {probeCheck?.status === 'unknown' && probeCheck.code === 'upstream_not_probed' && (
            <Text fz="xs" c="dimmed">
              Backend 不保留已过期的 Probe 记录，所以「从未执行」与「上次结论已过期」在 readiness 里
              完全同形，无法区分。下面的执行记录是唯一可查的旁证。
            </Text>
          )}

          {activeRun.data && (
            <Group gap="xs" wrap="wrap">
              <RunBadge status={activeRun.data.status} />
              <Text fz="xs" c="dimmed">
                {activeRun.data.progress.channels_finished} /{' '}
                {activeRun.data.progress.channels_total} 条线路已完成
              </Text>
            </Group>
          )}

          {failure && (
            <Box>
              <Text fz="sm">{failure.text}</Text>
              {failure.hint && (
                <Text fz="xs" c="dimmed" mt={2}>
                  {failure.hint}
                </Text>
              )}
            </Box>
          )}

          {probe.error ? <ErrorAlert error={probe.error} /> : null}
        </Stack>
      </CardRow>

      {history.length > 0 && (
        <>
          <CardRow>
            <Text fz="xs" c="dimmed">
              最近的 Probe 执行记录
            </Text>
          </CardRow>
          {history.map((run) => (
            <ProbeRunRow key={run.id} run={run} />
          ))}
        </>
      )}
    </SectionCard>
  )
}

function ProbeRunRow({ run }: { run: Run }) {
  const failure = run.last_error
    ? describeError(run.last_error.code, run.last_error.retryable)
    : undefined

  return (
    <CardRow>
      <Group justify="space-between" align="flex-start" wrap="wrap" gap="xs">
        <Box style={{ minWidth: 0 }}>
          <Group gap="xs" wrap="wrap">
            <RunBadge status={run.status} />
            <PageLink to={`/runs/${run.id}`}>查看这次执行</PageLink>
          </Group>
          {failure && (
            <Text fz="xs" mt={4}>
              {failure.text}
            </Text>
          )}
        </Box>
        <MetaLine gap="sm">
          <Fact label="开始">
            <Timestamp iso={run.created_at} relative />
          </Fact>
          {run.finished_at && (
            <Fact label="结束">
              <Timestamp iso={run.finished_at} />
            </Fact>
          )}
        </MetaLine>
      </Group>
    </CardRow>
  )
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

function ConfigPanel({ channel }: { channel: Channel }) {
  const fields: { label: string; value: ReactNode; hint?: string }[] = [
    { label: 'Channel ID', value: <IdChip value={channel.id} width={280} /> },
    { label: '显示名称', value: channel.display_name ?? '未设置' },
    { label: 'Source', value: channel.source },
    { label: 'RouteTemplate', value: channel.route_template_id },
    { label: 'Egress', value: channel.egress_profile_id ?? '未绑定', hint: '出站线路' },
  ]

  if (channel.endpoint_profile_id) {
    fields.push({ label: 'Endpoint', value: channel.endpoint_profile_id })
  }
  if (channel.credential_id) {
    fields.push({ label: 'Credential', value: channel.credential_id })
  }

  fields.push(
    { label: '优先级', value: String(channel.priority), hint: '同一 Source 下数值大的先被选用' },
    { label: '启用', value: channel.enabled ? '已启用' : '已停用' },
    { label: 'revision', value: String(channel.revision), hint: '并发校验依据' },
  )

  const parameters = channel.parameters ?? {}
  const hasParameters = Object.keys(parameters).length > 0

  return (
    <SectionCard title="配置">
      <CardRow>
        <FieldList fields={fields} />
      </CardRow>
      {hasParameters && (
        <CardRow>
          <Text fz="xs" c="dimmed" mb={6}>
            parameters
          </Text>
          <JsonBlock value={parameters} />
        </CardRow>
      )}
    </SectionCard>
  )
}

// ---------------------------------------------------------------------------
// Chrome authorization descriptor
// ---------------------------------------------------------------------------

/**
 * Only browser-backed Channels have a descriptor; for everything else the
 * Backend answers `409 scope_invalid`. Gate the request on RouteTemplate auth,
 * because a Chrome session permission is not a stored Credential.
 */
function ChromePanel({ channel }: { channel: Channel }) {
  const templates = useRouteTemplates()

  const cookieBacked = useMemo(
    () =>
      (templates.data ?? []).find(
        (entry) => entry.route_template_id === channel.route_template_id,
      )?.auth.kind === 'browser_cookie',
    [channel.route_template_id, templates.data],
  )

  const descriptor = useChromeDescriptor(cookieBacked ? channel.id : undefined)

  if (!cookieBacked || !descriptor.data) return null

  const { login_url, permission_origin_pattern, cookie_scope } = descriptor.data.data

  return (
    <SectionCard title="Chrome 授权范围">
      <CardRow>
        <Stack gap="sm">
          <Caveat>
            这条 Channel 会在当前 Chrome 登录会话中发出受限请求。真正的授权必须由你在 Chrome
            扩展里对该 origin 批准，OmniHub 无法代替你同意，也不会保存 Cookie。
          </Caveat>
          <FieldList
            fields={[
              {
                label: '登录地址',
                value: (
                  <Anchor href={login_url} target="_blank" rel="noreferrer" fz="sm">
                    {login_url}
                  </Anchor>
                ),
              },
              {
                label: '授权 origin',
                value: (
                  <Text fz="sm" ff="monospace">
                    {permission_origin_pattern}
                  </Text>
                ),
                hint: '需要在扩展里批准的范围',
              },
              {
                label: '会话地址',
                value: (
                  <Text fz="sm" ff="monospace">
                    {cookie_scope.url}
                  </Text>
                ),
              },
              { label: '涉及域名', value: cookie_scope.allowed_domains.join('，') || '未限定' },
              { label: '登录 Cookie', value: cookie_scope.names.join('，') || '未限定' },
              { label: '存储区', value: cookie_scope.store },
            ]}
          />
        </Stack>
      </CardRow>
    </SectionCard>
  )
}

// ---------------------------------------------------------------------------
// Edit
// ---------------------------------------------------------------------------

interface ParameterShape {
  writable: boolean
  url: string
  blockedBy: string[]
}

/**
 * PUT takes a flat `url` and rejects `parameters`, so only a Channel whose
 * parameters are exactly `{url}` (or empty) can be rewritten without losing
 * configuration. Anything else is reported instead of silently dropped.
 */
function inspectParameters(channel: Channel): ParameterShape {
  const parameters = channel.parameters ?? {}
  const blockedBy = Object.keys(parameters).filter((key) => key !== 'url')
  const url = typeof parameters.url === 'string' ? parameters.url : ''
  return { writable: blockedBy.length === 0, url, blockedBy }
}

function EditChannelModal({
  channel,
  etag,
  opened,
  onClose,
}: {
  channel: Channel
  etag: string | null
  opened: boolean
  onClose: () => void
}) {
  const update = useUpdate<ChannelInput, Channel>((id) => `/v1/channels/${id}`, [
    keys.channels,
    keys.channel(channel.id),
  ])
  const egress = useEgressProfiles()
  const shape = inspectParameters(channel)

  const [displayName, setDisplayName] = useState(channel.display_name ?? '')
  const [url, setUrl] = useState(shape.url)
  const [egressId, setEgressId] = useState(channel.egress_profile_id ?? '')
  const [priority, setPriority] = useState<number>(channel.priority)
  const [enabled, setEnabled] = useState(channel.enabled)

  // Re-seed from the Channel each time the dialog opens, so a cancelled edit
  // does not leave stale values behind for the next attempt.
  useEffect(() => {
    if (!opened) return
    setDisplayName(channel.display_name ?? '')
    setUrl(typeof channel.parameters?.url === 'string' ? channel.parameters.url : '')
    setEgressId(channel.egress_profile_id ?? '')
    setPriority(channel.priority)
    setEnabled(channel.enabled)
    update.reset()
  }, [opened, channel])

  const submit = async () => {
    if (!etag) return
    try {
      await update.mutateAsync({
        id: channel.id,
        etag,
        // Full replacement: every field the write model requires is resent,
        // mapped from the read model — `source` becomes `source_id`, and
        // `parameters.url` becomes a flat `url`.
        body: {
          id: channel.id,
          source_id: channel.source,
          route_template_id: channel.route_template_id,
          priority,
          enabled,
          display_name: displayName.trim() || undefined,
          url: url.trim() || undefined,
          egress_profile_id: egressId || undefined,
          endpoint_profile_id: channel.endpoint_profile_id || undefined,
          credential_id: channel.credential_id || undefined,
        },
      })
      onClose()
    } catch {
      // Kept open so the conflict or rejection stays next to the fields.
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title="编辑 Channel" size="lg" centered>
      <Stack gap="sm">
        {!shape.writable && (
          <Caveat>
            这条 Channel 的 parameters 还包含 {shape.blockedBy.join('，')}，而写接口只接受扁平的
            url、不接受 parameters。在这里保存会丢掉这些键，因此保存已被禁用，请改用 CLI 修改。
          </Caveat>
        )}
        {!etag && (
          <Caveat>还没有读到这条记录的 ETag，无法安全提交。请先刷新页面再编辑。</Caveat>
        )}

        <FieldList
          fields={[
            {
              label: 'Channel ID',
              value: (
                <Text fz="sm" ff="monospace">
                  {channel.id}
                </Text>
              ),
            },
            { label: 'Source', value: channel.source },
            { label: 'RouteTemplate', value: channel.route_template_id },
          ]}
        />
        <Text fz="xs" c="dimmed">
          以上三项决定了这条线路的身份，不能在这里更改。
        </Text>

        <TextInput
          label="显示名称"
          value={displayName}
          onChange={(event) => setDisplayName(event.currentTarget.value)}
        />
        <TextInput
          label="Feed 地址"
          description="公开可访问的 RSS 或 Atom 地址。"
          value={url}
          onChange={(event) => setUrl(event.currentTarget.value)}
          required
        />
        <Group grow align="flex-start">
          <Select
            label="Egress"
            description="出站线路。写接口要求必须绑定一个。"
            placeholder={egress.isLoading ? '正在读取…' : '选择 Egress'}
            data={(egress.data ?? []).map((profile) => ({
              value: profile.id,
              label: profile.display_name || profile.id,
            }))}
            value={egressId}
            onChange={(value) => setEgressId(value ?? '')}
            required
          />
          <NumberInput
            label="优先级"
            value={priority}
            onChange={(value) => setPriority(typeof value === 'number' ? value : 0)}
            min={0}
            max={1000}
          />
        </Group>
        <Switch
          label="启用这条 Channel"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        {update.error ? <ErrorAlert error={update.error} /> : null}

        <Text fz="xs" c="dimmed">
          保存只改写配置。改动 Egress 或地址会让已有的 Probe 结论失效，需要重新执行一次 Probe
          才能确认这条线路仍然可用。
        </Text>

        <Group justify="flex-end" gap="xs" mt="xs">
          <Button variant="default" onClick={onClose}>
            取消
          </Button>
          <Button
            onClick={() => void submit()}
            loading={update.isPending}
            disabled={READ_ONLY || !etag || !shape.writable || url.trim() === '' || egressId === ''}
          >
            保存
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

/**
 * Two steps: read the detail to capture its current ETag, then send that exact
 * validator. The read/confirm gap is where a `409 revision_conflict` becomes
 * meaningful, and it must fail loudly rather than overwrite.
 */
function DeleteChannelDialog({
  channel,
  opened,
  onClose,
}: {
  channel: Channel
  opened: boolean
  onClose: () => void
}) {
  const remove = useDelete((id) => `/v1/channels/${id}`, [keys.channels])
  const [etagError, setEtagError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setEtagError(null)
    try {
      const current = await get<Channel>(`/v1/channels/${channel.id}`)
      if (!current.etag) {
        throw new Error('Backend 没有返回 ETag，无法安全删除')
      }
      await remove.mutateAsync({ id: channel.id, etag: current.etag })
      onClose()
    } catch (error) {
      setEtagError(error)
    } finally {
      setPending(false)
    }
  }

  return (
    <ConfirmDialog
      opened={opened}
      onClose={onClose}
      onConfirm={() => void confirm()}
      title="删除 Channel"
      target={channel.display_name ?? channel.id}
      consequence="删除后这条线路不再参与任何查询。若仍有 View 引用它，Backend 会拒绝删除并告知你，不会连带改动那些 View。"
      loading={pending}
      error={etagError ?? remove.error}
    />
  )
}
