/**
 * Runs — every execution the Backend has recorded.
 *
 * Filtering is client-side because it has to be: `GET /v1/runs` accepts only
 * `limit`, and passing `?kind=` is refused as a contract violation. So the page
 * reads a window of Runs, narrows it locally, and says how large that window is
 * rather than implying it is the whole history.
 *
 * A row reports what a Run actually is. A `partial` shows its coverage numbers,
 * not a failure: on this instance a `partial` view_refresh has an empty `errors`
 * array and coverage of 20 returned out of 50 examined — the limit cut the window
 * off, nothing broke.
 */

import { useMemo, useState } from 'react'
import { Anchor, Button, Group, Select, Stack, Table, Text } from '@mantine/core'
import { IconRefresh } from '@tabler/icons-react'
import { Link } from 'react-router'
import { useChannels, useRuns, useViews } from '../api/queries'
import type { Run } from '../api/types'
import { RUN_STATUSES, isTerminalRun } from '../api/types'
import { RunBadge, Timestamp } from '../components/display'
import {
  EmptyState,
  ErrorAlert,
  Fact,
  LoadingRow,
  MetaLine,
  PageHeader,
  PageLink,
  SectionCard,
} from '../components/layout'
import { RUN_COPY } from '../domain/vocabulary'

/** Run kinds as a person reads them. Shared with RunDetail. */
export const RUN_KIND_COPY: Record<Run['kind'], { label: string; description: string }> = {
  query: {
    label: '搜索',
    description: '一次内容搜索。',
  },
  view_refresh: {
    label: '订阅更新',
    description: '为一个订阅更新内容。',
  },
  channel_probe: {
    label: '来源检查',
    description: '检查一个来源能否连接。',
  },
}

const WINDOW_OPTIONS = ['20', '50', '100']

export function Runs() {
  const [limit, setLimit] = useState('20')
  const [kind, setKind] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)

  const runs = useRuns(Number(limit))
  const channels = useChannels()
  const views = useViews()

  // Resource ids are machine ids; a person recognises the name they gave the thing.
  const nameOf = useMemo(() => {
    const map = new Map<string, string>()
    for (const channel of channels.data ?? []) {
      if (channel.display_name) map.set(channel.id, channel.display_name)
    }
    for (const entry of views.data ?? []) {
      map.set(entry.view.id, entry.view.display_name)
    }
    return map
  }, [channels.data, views.data])

  const all = runs.data ?? []
  const list = all.filter(
    (run) => (!kind || run.kind === kind) && (!status || run.status === status),
  )
  const filtered = Boolean(kind || status)

  return (
    <Stack gap="lg">
      <PageHeader
        title="活动"
        description="查看订阅更新和来源检查的结果。"
        actions={
          <Button
            variant="default"
            leftSection={<IconRefresh size={14} />}
            onClick={() => void runs.refetch()}
            loading={runs.isFetching}
          >
            刷新
          </Button>
        }
      />

      {runs.error ? <ErrorAlert error={runs.error} onRetry={() => void runs.refetch()} /> : null}

      <SectionCard
        title="最近活动"
        count={
          all.length > 0
            ? filtered
              ? `${list.length} 条，筛选自最近 ${all.length} 条`
              : `最近 ${all.length} 条`
            : undefined
        }
        action={
          <Group gap="xs" wrap="wrap" align="flex-end">
            <Select
              aria-label="按类型筛选"
              placeholder="全部类型"
              data={Object.entries(RUN_KIND_COPY).map(([value, copy]) => ({
                value,
                label: copy.label,
              }))}
              value={kind}
              onChange={setKind}
              clearable
              w={150}
              size="xs"
            />
            <Select
              aria-label="按状态筛选"
              placeholder="全部状态"
              data={RUN_STATUSES.map((value) => ({ value, label: RUN_COPY[value].label }))}
              value={status}
              onChange={setStatus}
              clearable
              w={130}
              size="xs"
            />
            <Select
              aria-label="读取条数"
              data={WINDOW_OPTIONS.map((value) => ({ value, label: `最近 ${value} 条` }))}
              value={limit}
              onChange={(value) => setLimit(value ?? '20')}
              w={120}
              size="xs"
              allowDeselect={false}
            />
          </Group>
        }
      >
        {runs.isLoading ? (
          <LoadingRow />
        ) : all.length === 0 ? (
          <EmptyState
            title="还没有活动记录。"
            hint="更新一个订阅或检查一个来源后，记录会出现在这里。"
            action={<PageLink to="/subscriptions">去更新订阅</PageLink>}
          />
        ) : list.length === 0 ? (
          <EmptyState
            title="最近的记录里没有符合这些条件的执行。"
            hint="放宽筛选条件，或读取更多条记录。"
          />
        ) : (
          <Table.ScrollContainer minWidth={820}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>目标</Table.Th>
                  <Table.Th>类型</Table.Th>
                  <Table.Th>结果</Table.Th>
                  <Table.Th>创建时间</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {list.map((run) => (
                  <RunRow key={run.id} run={run} name={nameOf.get(run.resource.id)} />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>
    </Stack>
  )
}

function RunRow({ run, name }: { run: Run; name?: string }) {
  return (
    <Table.Tr>
      <Table.Td>
        <Anchor component={Link} to={`/activity/${run.id}`} fz="sm" fw={500}>
          {name ?? run.resource.id}
        </Anchor>
      </Table.Td>
      <Table.Td>
        <Text fz="sm">{RUN_KIND_COPY[run.kind]?.label ?? run.kind}</Text>
        {run.attempt > 1 && (
          <Text fz="xs" c="dimmed">
            第 {run.attempt} 次尝试
          </Text>
        )}
      </Table.Td>
      <Table.Td>
        <Stack gap={4} align="flex-start">
          <RunBadge status={run.status} />
          <RunOutcomeLine run={run} />
        </Stack>
      </Table.Td>
      <Table.Td>
        <Timestamp iso={run.created_at} relative />
        {run.result && (
          <Text fz="xs" c="dimmed">
            耗时 {run.result.meta.duration_ms} ms
          </Text>
        )}
      </Table.Td>
    </Table.Tr>
  )
}

/**
 * One line of evidence per Run. In-flight Runs report progress; finished ones
 * report what came back. Coverage gaps and route failures are counted separately,
 * so a `partial` with an empty `errors` array never reads as a failure.
 */
function RunOutcomeLine({ run }: { run: Run }) {
  if (!isTerminalRun(run.status)) {
    return (
      <MetaLine gap="sm">
        <Fact label="已完成线路">{run.progress.channels_finished}</Fact>
        <Fact label="选中线路">{run.progress.channels_total}</Fact>
      </MetaLine>
    )
  }

  if (run.last_error) {
    return (
      <Text fz="xs" c="dimmed">
        失败原因见详情
      </Text>
    )
  }

  if (!run.result) {
    // A completed channel_probe carries no Envelope: readiness is the outcome.
    return null
  }

  const returned = run.result.coverage.reduce((total, entry) => total + entry.returned, 0)
  const examined = run.result.coverage.reduce((total, entry) => total + entry.examined, 0)
  const gaps = run.result.coverage.filter((entry) => entry.truncated || !entry.exhaustive)
  const failures = run.result.executions.filter((entry) => entry.status === 'failed').length
  return (
    <MetaLine gap="sm">
      <Fact label="结果">{run.result.items.length} 条</Fact>
      {gaps.length > 0 && (
        <>
          <Fact label="返回">{returned} 条</Fact>
          <Fact label="检查过">{examined} 条</Fact>
        </>
      )}
      {failures > 0 && <Fact label="失败线路">{failures} 条</Fact>}
    </MetaLine>
  )
}
