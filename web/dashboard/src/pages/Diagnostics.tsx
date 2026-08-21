/**
 * Diagnostics — the whole instance's health in one read.
 *
 * This is the one surface where raw codes belong, because the reader is here to
 * trace a specific failure. They stay in a dimmed monospace footnote: the
 * headline is always the consequence.
 *
 * Three judgments shape the page:
 *
 *   **Grouped by state, worst first.** A flat list makes the reader compare 8
 *   readiness values by hand. Grouping puts everything that needs attention at
 *   the top, and the states with no channels are absent rather than shown as
 *   zero — the same sparseness the Backend uses.
 *
 *   **"Unverified" is not "broken", and it is not "unprobeable" either.** All
 *   three arrive as `degraded`. A Feed Channel with no valid Probe simply has not
 *   been checked recently; a GitHub or xurl Channel reports `probe_unsupported`
 *   and can never be checked at all. Offering a Probe button for the latter would
 *   claim the check is merely pending, so it is withheld and the reason is stated.
 *
 *   **`route_groups[]` is empty on a healthy instance and that is not a gap.**
 *   `aggregateRouteGroups` only emits a group once one route group has probe
 *   records under two or more Egress profiles with opposite outcomes. The empty
 *   state explains that condition rather than implying data is missing.
 */

import { useEffect, useMemo } from 'react'
import { Box, Button, Group, Stack, Text } from '@mantine/core'
import { IconRefresh, IconStethoscope } from '@tabler/icons-react'
import {
  READ_ONLY,
  useChannels,
  useProbeChannel,
  useReadiness,
  useRun,
} from '../api/queries'
import type {
  Channel,
  ChannelHealth,
  ReadinessCheck,
  ReadinessState,
  RouteGroupHealth,
} from '../api/types'
import { READINESS_STATES, isTerminalRun } from '../api/types'
import { IdChip, ReadinessBadge, RunBadge, Timestamp } from '../components/display'
import {
  CardRow,
  Caveat,
  DemoModeNotice,
  EmptyState,
  ErrorAlert,
  Fact,
  FieldList,
  LoadingRow,
  MetaLine,
  PageHeader,
  PageLink,
  SectionCard,
} from '../components/layout'
import { CHECK_KIND_COPY, READINESS_COPY, describeCheckCode, describeError } from '../domain/vocabulary'

// ---------------------------------------------------------------------------
// Page-local vocabulary
// ---------------------------------------------------------------------------

const ACTION_KIND_COPY: Record<string, string> = {
  grant_browser_permission: '需要在 Chrome 扩展里授予该站点的读取权限。',
  install_dependency: '需要先安装这个 Adapter 依赖的命令行工具。',
  review_template: '需要确认是否信任这个 RouteTemplate。',
  start_chrome_bridge: '需要先让 Chrome 扩展连上 OmniHub。',
}

/** Worst first: a reader opens this page because something is wrong. */
const STATE_ORDER: ReadinessState[] = [
  'blocked',
  'needs_login',
  'needs_permission',
  'not_configured',
  'degraded',
  'unknown',
  'ready_dependent',
  'ready',
]

// ---------------------------------------------------------------------------
// Evidence selection
// ---------------------------------------------------------------------------

/**
 * The check that explains the current state: a real failure if there is one,
 * otherwise the first unresolved layer. A passing ladder yields nothing, which
 * is correct — a ready Channel has no failing check to cite.
 */
function decisiveCheck(health: ChannelHealth): ReadinessCheck | undefined {
  return (
    health.checks.find((check) => check.status === 'failed') ??
    health.checks.find((check) => check.status === 'unknown')
  )
}

function isUnprobeable(health: ChannelHealth): boolean {
  return health.checks.some(
    (check) => check.kind === 'channel_probe' && check.code === 'probe_unsupported',
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function Diagnostics() {
  const readiness = useReadiness()
  const channels = useChannels()

  const nameOf = useMemo(() => {
    const map = new Map<string, Channel>()
    for (const channel of channels.data ?? []) map.set(channel.id, channel)
    return map
  }, [channels.data])

  const health = readiness.data?.channels ?? []

  // Sparse on purpose: a state with no channels is absent, not zero.
  const grouped = useMemo(() => {
    const buckets = new Map<ReadinessState, ChannelHealth[]>()
    for (const entry of health) {
      const known = READINESS_STATES.includes(entry.readiness) ? entry.readiness : 'unknown'
      const bucket = buckets.get(known)
      if (bucket) bucket.push(entry)
      else buckets.set(known, [entry])
    }
    return STATE_ORDER.filter((state) => buckets.has(state)).map((state) => ({
      state,
      entries: buckets.get(state)!,
    }))
  }, [health])

  return (
    <Stack gap="lg">
      <PageHeader
        title="Diagnostics"
        description="这里汇总了每条 Channel 当前的判定依据，以及同一 route group 在不同 Egress 下的差异。"
        actions={
          <Button
            variant="default"
            leftSection={<IconRefresh size={14} />}
            loading={readiness.isFetching}
            onClick={() => {
              void readiness.refetch()
              void channels.refetch()
            }}
          >
            重新评估
          </Button>
        }
      />

      <MetaLine gap="md">
        <PageLink to="/channels">管理 Channels</PageLink>
        <PageLink to="/runs">查看执行记录</PageLink>
      </MetaLine>

      <DemoModeNotice />
      {readiness.error ? (
        <ErrorAlert error={readiness.error} onRetry={() => void readiness.refetch()} />
      ) : null}

      <AssessmentMeta
        generatedAt={readiness.data?.generated_at}
        total={health.length}
        fetching={readiness.isFetching}
      />

      {readiness.isLoading ? (
        <SectionCard title="线路健康">
          <LoadingRow label="正在评估…" />
        </SectionCard>
      ) : health.length === 0 ? (
        <SectionCard title="线路健康">
          <EmptyState
            title="没有可评估的 Channel。"
            hint="readiness 只对已注册的 Channel 给出结论。先创建一条线路，再回到这里查看它的判定依据。"
            action={<PageLink to="/channels">前往 Channels</PageLink>}
          />
        </SectionCard>
      ) : (
        grouped.map(({ state, entries }) => (
          <StateGroup key={state} state={state} entries={entries} nameOf={nameOf} />
        ))
      )}

      <RouteGroupPanel
        groups={readiness.data?.route_groups ?? []}
        loading={readiness.isLoading}
      />
    </Stack>
  )
}

// ---------------------------------------------------------------------------
// Freshness of the assessment itself
// ---------------------------------------------------------------------------

function AssessmentMeta({
  generatedAt,
  total,
  fetching,
}: {
  generatedAt?: string
  total: number
  fetching: boolean
}) {
  if (!generatedAt) return null
  return (
    <MetaLine gap="md">
      <Fact label="本次评估生成于">
        <Timestamp iso={generatedAt} />
      </Fact>
      <Fact label="覆盖线路">{total} 条</Fact>
      {fetching && (
        <Text fz="xs" c="dimmed">
          正在重新评估
        </Text>
      )}
    </MetaLine>
  )
}

// ---------------------------------------------------------------------------
// One readiness state
// ---------------------------------------------------------------------------

function StateGroup({
  state,
  entries,
  nameOf,
}: {
  state: ReadinessState
  entries: ChannelHealth[]
  nameOf: Map<string, Channel>
}) {
  const copy = READINESS_COPY[state]

  return (
    <SectionCard
      title={copy.label}
      count={`${entries.length} 条`}
      action={
        <Text fz="xs" c="dimmed" style={{ maxWidth: 420, textAlign: 'right' }}>
          {copy.meaning}
        </Text>
      }
    >
      {entries.map((entry) => (
        <ChannelHealthRow key={entry.channel_id} health={entry} channel={nameOf.get(entry.channel_id)} />
      ))}
    </SectionCard>
  )
}

function ChannelHealthRow({
  health,
  channel,
}: {
  health: ChannelHealth
  channel?: Channel
}) {
  const probe = useProbeChannel()
  const readiness = useReadiness()
  const activeRun = useRun(probe.data?.id, Boolean(probe.data))

  // Only a settled Run can change readiness, so re-read once it terminates.
  const status = activeRun.data?.status
  useEffect(() => {
    if (status && isTerminalRun(status)) void readiness.refetch()
  }, [status])

  const check = decisiveCheck(health)
  const codeCopy = describeCheckCode(check?.code)
  const unprobeable = isUnprobeable(health)
  const failure = check?.error ? describeError(check.error.code, check.error.retryable) : undefined
  const runFailure = activeRun.data?.last_error
    ? describeError(activeRun.data.last_error.code, activeRun.data.last_error.retryable)
    : undefined

  return (
    <CardRow>
      <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
        <Box style={{ minWidth: 240, flex: '1 1 420px' }}>
          <Group gap="xs" wrap="wrap">
            <PageLink to={`/channels/${health.channel_id}`}>
              {channel?.display_name ?? health.channel_id}
            </PageLink>
            <ReadinessBadge state={health.readiness} />
            {activeRun.data && <RunBadge status={activeRun.data.status} />}
          </Group>

          <Box mt={2}>
            <IdChip value={health.channel_id} width={240} />
          </Box>

          {check && (
            <Box mt={6}>
              <Text fz="sm">
                {CHECK_KIND_COPY[check.kind] ?? '附加检查'}
                {check.status === 'failed' ? ' 未通过' : ' 未验证'}
              </Text>
              {codeCopy && (
                <Box mt={2}>
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
            </Box>
          )}

          {health.action_required && (
            <Text fz="xs" mt={6}>
              {ACTION_KIND_COPY[health.action_required.kind] ?? '需要先完成一步手动操作。'}
            </Text>
          )}

          {runFailure && (
            <Text fz="xs" mt={6}>
              这次 Probe：{runFailure.text}
            </Text>
          )}

          <MetaLine gap="md">
            {channel && <Fact label="Source">{channel.source}</Fact>}
            {health.last_successful_probe_at && (
              <Fact label="上次成功 Probe">
                <Timestamp iso={health.last_successful_probe_at} relative />
              </Fact>
            )}
            {check?.expires_at && (
              <Fact label="结论有效至">
                <Timestamp iso={check.expires_at} />
              </Fact>
            )}
          </MetaLine>

          {/* The one page where raw codes are appropriate — dimmed, and last. */}
          {check && (check.code || check.error) && (
            <Text fz={10} c="dimmed" ff="monospace" mt={4}>
              {[health.readiness, check.kind, check.code, check.error?.code]
                .filter(Boolean)
                .join(' ')}
            </Text>
          )}

          {probe.error ? (
            <Box mt="xs">
              <ErrorAlert error={probe.error} />
            </Box>
          ) : null}
        </Box>

        <Group gap={4} wrap="nowrap" justify="flex-end">
          {unprobeable ? (
            <Text fz="xs" c="dimmed" style={{ maxWidth: 200, textAlign: 'right' }}>
              这个 Provider 无法探测
            </Text>
          ) : (
            <Button
              variant="default"
              size="compact-sm"
              leftSection={<IconStethoscope size={13} />}
              disabled={READ_ONLY}
              loading={probe.isPending}
              onClick={() => probe.mutate(health.channel_id)}
            >
              重新 Probe
            </Button>
          )}
        </Group>
      </Group>
    </CardRow>
  )
}

// ---------------------------------------------------------------------------
// Route groups
// ---------------------------------------------------------------------------

function RouteGroupPanel({
  groups,
  loading,
}: {
  groups: RouteGroupHealth[]
  loading: boolean
}) {
  return (
    <SectionCard
      title="Egress 差异"
      count={groups.length > 0 ? `${groups.length} 组` : undefined}
    >
      {loading ? (
        <LoadingRow />
      ) : groups.length === 0 ? (
        <EmptyState
          title="当前没有出现 Egress 之间的差异。"
          hint="只有当同一个 route group 在两个以上 Egress 下都有 Probe 记录、并且结果相反时，这里才会出现条目：那种情况下线路的可用性取决于走哪个出口，判定结果是「部分线路可用」。现在没有这样的记录，说明还没有对同一组线路用不同 Egress 各探测过一次。"
        />
      ) : (
        groups.map((group) => <RouteGroupRow key={group.route_group} group={group} />)
      )}
    </SectionCard>
  )
}

function RouteGroupRow({ group }: { group: RouteGroupHealth }) {
  return (
    <CardRow>
      <Stack gap="sm">
        <Group gap="xs" wrap="wrap">
          <Text fz="sm" fw={500} ff="monospace">
            {group.route_group}
          </Text>
          <ReadinessBadge state={group.readiness} />
        </Group>

        <Caveat>
          这组线路在下列 Egress 下结果相反，因此它是否可用取决于实际走哪个出口。
        </Caveat>

        <FieldList
          fields={[
            {
              label: '成功的 Egress',
              value: group.ready_egress_profile_ids.join('，') || '无',
            },
            {
              label: '失败的 Egress',
              value: group.failed_egress_profile_ids.join('，') || '无',
            },
            {
              label: '涉及 Channel',
              value: (
                <Stack gap={2}>
                  {group.channel_ids.map((id) => (
                    <PageLink key={id} to={`/channels/${id}`}>
                      {id}
                    </PageLink>
                  ))}
                </Stack>
              ),
            },
          ]}
        />
      </Stack>
    </CardRow>
  )
}
