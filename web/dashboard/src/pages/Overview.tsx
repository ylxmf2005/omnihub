/**
 * Overview.
 *
 * This page answers two questions and nothing else:
 *   1. Is my setup working?
 *   2. What, if anything, needs me?
 *
 * Deliberately NOT here: probe layer ladders, route-group hashes, resolved IPs,
 * raw error strings, snapshot IDs, egress internals. Those are diagnosis material
 * and live on Channel detail, Diagnostics and Run detail. On the homepage they
 * made the first screen read like a log dump.
 *
 * Subscriptions and recent runs are stacked full width rather than placed side by
 * side: they hold different counts, so two columns left the shorter card's bottom
 * edge hanging over dead space.
 */

import {
  Alert,
  Anchor,
  Badge,
  Box,
  Button,
  Card,
  Group,
  Progress,
  SimpleGrid,
  Stack,
  Text,
  Tooltip,
} from '@mantine/core'
import {
  IconCircleCheck,
  IconExternalLink,
  IconRefresh,
  IconStethoscope,
} from '@tabler/icons-react'
import { Link } from 'react-router'
import {
  useBridge,
  useChannels,
  useProbeChannel,
  useReadiness,
  useRefreshView,
  useRuns,
  useSummary,
  useViews,
  READ_ONLY,
} from '../api/queries'
import type { ChannelHealth, ReadinessState } from '../api/types'
import { READINESS_COPY, describeError } from '../domain/vocabulary'
import { IdChip, ReadinessBadge, RunBadge, Timestamp, ViewBadge } from '../components/display'
import {
  CardRow,
  EmptyState,
  ErrorAlert,
  Fact,
  MetaLine,
  PageHeader,
  PageLink,
  SectionCard,
} from '../components/layout'

/** Only these need a person to act; the rest are healthy or self-resolving. */
const NEEDS_ACTION: ReadinessState[] = [
  'needs_login',
  'needs_permission',
  'blocked',
  'not_configured',
  'degraded',
]

const RUN_KIND_COPY: Record<string, string> = {
  view_refresh: '刷新订阅',
  channel_probe: '连通性检查',
  query: '查询',
}

export function Overview() {
  const summary = useSummary()
  const readiness = useReadiness()
  const views = useViews()
  const runs = useRuns()
  const channels = useChannels()
  const bridge = useBridge()

  const channelNames = new Map(
    (channels.data ?? []).map((channel) => [channel.id, channel.display_name ?? channel.id]),
  )

  const health = readiness.data?.channels ?? []
  const readyCount = health.filter(
    (channel) => channel.readiness === 'ready' || channel.readiness === 'ready_dependent',
  ).length
  const attention = health.filter((channel) => NEEDS_ACTION.includes(channel.readiness))

  const viewList = views.data ?? []
  const runList = runs.data ?? []
  const allHealthy = attention.length === 0

  // Derived from the Run list rather than summary.active_runs: the headline is
  // "what happened recently", and a live instance is usually idle at 0 active.
  const dayAgo = Date.now() - 24 * 60 * 60 * 1000
  const recentRuns = runList.filter((run) => new Date(run.created_at).getTime() >= dayAgo)
  const failedRuns = recentRuns.filter((run) => run.status === 'failed').length

  const refreshAll = () => {
    void summary.refetch()
    void readiness.refetch()
    void views.refetch()
    void runs.refetch()
    void bridge.refetch()
  }

  return (
    <Stack gap="lg">
      <PageHeader
        title="概览"
        description={
          summary.data ? (
            <>
              更新于 <Timestamp iso={summary.data.generated_at} />
            </>
          ) : (
            '正在读取…'
          )
        }
        actions={
          <Button
            variant="default"
            leftSection={<IconRefresh size={14} />}
            onClick={refreshAll}
            loading={readiness.isFetching}
          >
            刷新
          </Button>
        }
      />

      {readiness.error ? <ErrorAlert error={readiness.error} onRetry={refreshAll} /> : null}

      {/* ---- Three comparable readings of the same instance ---- */}
      <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="md">
        <StatCard
          label="来源接入"
          value={`${readyCount}/${health.length}`}
          caption={
            health.length === 0
              ? '尚未创建 Channel'
              : allHealthy
                ? '全部可用'
                : `${attention.length} 个待处理`
          }
          tone={health.length === 0 ? 'idle' : allHealthy ? 'ok' : 'warn'}
          bar={
            health.length > 0
              ? { ok: readyCount / health.length, warn: attention.length / health.length }
              : undefined
          }
        />
        <StatCard
          label="订阅内容"
          value={String(viewList.length)}
          caption={viewList.length === 0 ? '尚未创建 View' : '个 View 正在分发 Feed'}
          tone={viewList.length === 0 ? 'idle' : 'ok'}
          bar={
            viewList.length > 0
              ? {
                  ok: viewList.filter((entry) => entry.status === 'fresh').length / viewList.length,
                  warn:
                    viewList.filter(
                      (entry) => entry.status === 'stale' || entry.status === 'failed',
                    ).length / viewList.length,
                }
              : undefined
          }
          detail={
            viewList.length > 0
              ? `${viewList.filter((entry) => entry.status === 'fresh').length} 个内容为最新`
              : undefined
          }
        />
        <StatCard
          label="近 24 小时执行"
          value={String(recentRuns.length)}
          caption={recentRuns.length === 0 ? '暂无执行记录' : '次执行'}
          tone={failedRuns > 0 ? 'warn' : recentRuns.length === 0 ? 'idle' : 'ok'}
          bar={
            recentRuns.length > 0
              ? {
                  ok: (recentRuns.length - failedRuns) / recentRuns.length,
                  warn: failedRuns / recentRuns.length,
                }
              : undefined
          }
          detail={
            recentRuns.length === 0
              ? undefined
              : failedRuns > 0
                ? `${failedRuns} 次失败`
                : '全部成功'
          }
        />
      </SimpleGrid>

      {/* ---- What needs you ---- */}
      {attention.length > 0 ? (
        <SectionCard
          title="需要处理"
          count={`${attention.length} 项`}
          action={<PageLink to="/diagnostics">打开 Diagnostics</PageLink>}
        >
          <Stack gap={0}>
            {attention.map((item) => (
              <AttentionRow
                key={item.channel_id}
                health={item}
                name={channelNames.get(item.channel_id) ?? item.channel_id}
              />
            ))}
          </Stack>
        </SectionCard>
      ) : (
        health.length > 0 && (
          <Alert
            variant="light"
            color="teal"
            icon={<IconCircleCheck size={16} />}
            title="目前没有需要处理的问题"
          >
            <Text fz="sm">所有已启用的 Channel 最近一次 Probe 都已通过。</Text>
          </Alert>
        )
      )}

      <SectionCard title="我的订阅" action={<PageLink to="/views">全部 View</PageLink>}>
        <Stack gap={0}>
          {viewList.slice(0, 5).map((entry) => (
            <SubscriptionRow key={entry.view.id} entry={entry} />
          ))}
          {viewList.length === 0 && (
            <EmptyState
              title="还没有 View。"
              hint="View 保存一条查询，并把结果发布为 JSON、RSS、Atom Feed。"
              action={
                <Button component={Link} to="/views" variant="default">
                  创建 View
                </Button>
              }
            />
          )}
        </Stack>
      </SectionCard>

      <SectionCard title="最近执行" action={<PageLink to="/runs">全部 Run</PageLink>}>
        <Stack gap={0}>
          {runList.slice(0, 5).map((run) => (
            <CardRow key={run.id}>
              <Group justify="space-between" wrap="nowrap" gap="sm">
                <Box style={{ minWidth: 0 }}>
                  <Group gap="xs" wrap="nowrap">
                    <Anchor component={Link} to={`/runs/${run.id}`} fz="sm" truncate>
                      {RUN_KIND_COPY[run.kind] ?? run.kind}
                    </Anchor>
                    <Text fz="sm" c="dimmed" truncate>
                      {channelNames.get(run.resource.id) ??
                        viewList.find((entry) => entry.view.id === run.resource.id)?.view
                          .display_name ??
                        run.resource.id}
                    </Text>
                  </Group>
                  <Text fz="xs" c="dimmed">
                    <Timestamp iso={run.created_at} relative />
                  </Text>
                </Box>
                <RunBadge status={run.status} />
              </Group>
            </CardRow>
          ))}
          {runList.length === 0 && <EmptyState title="还没有执行记录。" />}
        </Stack>
      </SectionCard>

      {/* A never-connected Bridge on a setup with no Cookie-dependent Channel is
          not a problem, so it gets one quiet line rather than a panel with a
          four-item troubleshooting list. */}
      {bridge.data && !bridge.data.connected && (
        <Text fz="xs" c="dimmed">
          Chrome Bridge 未连接，依赖登录 Cookie 的来源暂时不可用。
          <Anchor component={Link} to="/bridge" fz="xs" ml={8}>
            查看 Browser Bridge
          </Anchor>
        </Text>
      )}
    </Stack>
  )
}

/**
 * One reading of the instance. The number is always neutral in colour — a count
 * is not good or bad on its own; the caption carries the verdict. Every card has
 * the same four parts so three of them read as one comparable row instead of one
 * full card beside two empty ones.
 */
function StatCard({
  label,
  value,
  caption,
  tone,
  bar,
  detail,
}: {
  label: string
  value: string
  caption: string
  tone: 'ok' | 'warn' | 'idle'
  bar?: { ok: number; warn: number }
  detail?: string
}) {
  const color = tone === 'ok' ? 'teal' : tone === 'warn' ? 'yellow' : 'gray'
  return (
    <Card>
      <Text fz="xs" c="dimmed">
        {label}
      </Text>
      <Group align="baseline" gap={8} mt={6}>
        <Text ff="monospace" fz={28} fw={600} lh={1}>
          {value}
        </Text>
        <Text fz="xs" c="dimmed">
          {caption}
        </Text>
      </Group>

      <Progress.Root size={6} mt={12} bg="var(--mantine-color-default-border)">
        {bar && (
          <>
            <Progress.Section value={bar.ok * 100} color="teal" />
            <Progress.Section value={bar.warn * 100} color="yellow" />
          </>
        )}
      </Progress.Root>

      <Text fz="xs" c={detail && tone === 'warn' ? color : 'dimmed'} mt={6}>
        {detail ?? ' '}
      </Text>
    </Card>
  )
}

function SubscriptionRow({
  entry,
}: {
  entry: ReturnType<typeof useViews>['data'] extends (infer T)[] | undefined ? T : never
}) {
  const refresh = useRefreshView()

  return (
    <CardRow>
      <Group justify="space-between" wrap="wrap" gap="md">
        <Box style={{ minWidth: 0, flex: '1 1 320px' }}>
          <Group gap="xs" wrap="nowrap">
            <Anchor component={Link} to={`/views/${entry.view.id}`} fz="md" fw={500} truncate>
              {entry.view.display_name}
            </Anchor>
            <ViewBadge status={entry.status} />
          </Group>
          <MetaLine>
            {entry.snapshot ? (
              <>
                <Fact label="内容更新于">
                  <Timestamp iso={entry.snapshot.created_at} relative />
                </Fact>
                <Fact label="新鲜期至">
                  <Timestamp iso={entry.snapshot.fresh_until} />
                </Fact>
              </>
            ) : (
              <Text fz="xs" c="dimmed">
                尚无快照
              </Text>
            )}
          </MetaLine>
        </Box>
        <Group gap={6} wrap="nowrap">
          {/* Feed URLs are taken from the API, never assembled here. */}
          {(['json', 'rss', 'atom'] as const).map((kind) => (
            <Tooltip key={kind} label={entry.feed_urls[kind]} fz="xs">
              <Anchor
                href={entry.feed_urls[kind]}
                fz={10}
                ff="monospace"
                tt="uppercase"
                c="dimmed"
                px={5}
                style={{
                  border: '1px solid var(--mantine-color-default-border)',
                  borderRadius: 3,
                }}
              >
                {kind}
              </Anchor>
            </Tooltip>
          ))}
          <Button
            variant="default"
            size="compact-xs"
            leftSection={<IconRefresh size={12} />}
            disabled={READ_ONLY}
            loading={refresh.isPending}
            onClick={() => refresh.mutate(entry.view.id)}
          >
            刷新
          </Button>
        </Group>
      </Group>
      {refresh.error ? (
        <Box mt="xs">
          <ErrorAlert error={refresh.error} />
        </Box>
      ) : null}
    </CardRow>
  )
}

/**
 * One actionable problem: what is wrong in plain language, what it means, and the
 * action that resolves it. The raw code stays in the badge tooltip and on the
 * detail page.
 */
function AttentionRow({ health, name }: { health: ChannelHealth; name: string }) {
  const probe = health.checks.find((check) => check.kind === 'channel_probe')
  const error = probe?.error
  const described = error ? describeError(error.code, error.retryable) : null
  const action = health.action_required
  const reprobe = useProbeChannel()

  return (
    <CardRow>
      <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
        {/* Wraps on narrow screens so the explanation keeps a readable measure
            instead of being squeezed into a column beside the buttons. */}
        <Box style={{ minWidth: 0, flex: '1 1 320px' }}>
          <Group gap="xs" wrap="wrap">
            <Anchor
              component={Link}
              to={`/channels/${health.channel_id}`}
              fz="md"
              fw={500}
            >
              {name}
            </Anchor>
            <ReadinessBadge state={health.readiness} />
          </Group>

          <Text fz="sm" c="dimmed" mt={4}>
            {described?.text ?? READINESS_COPY[health.readiness].meaning}
            {described?.hint && (
              <Text span fz="sm" c="dimmed">
                {' '}
                {described.hint}
              </Text>
            )}
          </Text>

          <MetaLine gap="sm">
            <IdChip value={health.channel_id} width={190} />
            {probe?.checked_at && (
              <Fact label="检查于">
                <Timestamp iso={probe.checked_at} relative />
              </Fact>
            )}
            {error?.retryable && (
              <Badge variant="default" size="xs" styles={{ label: { fontWeight: 400 } }}>
                可重试
              </Badge>
            )}
          </MetaLine>
        </Box>

        <Group gap="xs" wrap="nowrap">
          {/* A login link is only ever taken from action_required.url. */}
          {action?.url && (
            <Button
              component="a"
              href={action.url}
              target="_blank"
              rel="noreferrer"
              variant="light"
              rightSection={<IconExternalLink size={12} />}
            >
              前往登录
            </Button>
          )}
          <Button
            variant="default"
            leftSection={<IconStethoscope size={14} />}
            disabled={READ_ONLY}
            loading={reprobe.isPending}
            onClick={() => reprobe.mutate(health.channel_id)}
          >
            重新检查
          </Button>
          <Button component={Link} to={`/channels/${health.channel_id}`} variant="subtle" px={8}>
            详情
          </Button>
        </Group>
      </Group>
      {reprobe.error ? (
        <Box mt="xs">
          <ErrorAlert error={reprobe.error} />
        </Box>
      ) : null}
    </CardRow>
  )
}
