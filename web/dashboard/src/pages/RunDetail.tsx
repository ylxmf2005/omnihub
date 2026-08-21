/**
 * RunDetail — one execution, in full.
 *
 * Two things this page refuses to do:
 *
 *   **Force one layout onto every kind.** A `view_refresh` carries a whole
 *   Envelope; a `channel_probe` carries none at all — on the live instance a
 *   completed probe has no `result` and no `request`, because its outcome is the
 *   Channel's readiness, not a set of Items. So the page renders what is actually
 *   present and explains the absence instead of drawing empty sections.
 *
 *   **Read a `partial` as a failure.** Coverage gaps and route failures are
 *   separate sections with separate wording; see `EnvelopeView`.
 *
 * Polling is `useRun`'s job — it follows the Run to a terminal status and then
 * stops — so there is no interval here.
 */

import { Anchor, Stack, Text } from '@mantine/core'
import { Link, useParams } from 'react-router'
import { READ_ONLY, useRun, useRuns } from '../api/queries'
import type { OmniError, Run } from '../api/types'
import { isTerminalRun } from '../api/types'
import { EnvelopeView } from '../components/EnvelopeView'
import { IdChip, RunBadge, Timestamp } from '../components/display'
import {
  CardRow,
  Caveat,
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
import { RUN_COPY, describeError } from '../domain/vocabulary'
import { RUN_KIND_COPY } from './Runs'

export function RunDetail() {
  const { id } = useParams<{ id: string }>()

  const detail = useRun(id)
  // `useRun` is live-only. The list read is fixture-backed, so in demo mode it is
  // the only source for a single Run — and in live mode it is the same query key
  // the Runs page already filled, so a deep link costs at most one cached read.
  const list = useRuns(20)
  const run = detail.data ?? list.data?.find((entry) => entry.id === id)

  if (!run) {
    return (
      <Stack gap="lg">
        <PageHeader title="Run" />
        {detail.error ? (
          <ErrorAlert error={detail.error} onRetry={() => void detail.refetch()} />
        ) : detail.isFetching || list.isLoading ? (
          <LoadingRow />
        ) : (
          <EmptyState
            title="找不到这条 Run。"
            hint={
              READ_ONLY
                ? '演示模式只带有随仓库保存的少量 Run 快照。'
                : '它可能已经超出了 Backend 保留的记录范围。'
            }
            action={<PageLink to="/runs">返回 Runs</PageLink>}
          />
        )}
      </Stack>
    )
  }

  const kind = RUN_KIND_COPY[run.kind]

  return (
    <Stack gap="lg">
      <PageHeader
        title={kind?.label ?? run.kind}
        description={kind?.description}
        actions={
          <Anchor component={Link} to="/runs" fz="sm">
            返回 Runs
          </Anchor>
        }
      />

      <SectionCard title="执行概况">
        <CardRow>
          <FieldList
            fields={[
              {
                label: '状态',
                value: (
                  <Stack gap={4} align="flex-start">
                    <RunBadge status={run.status} />
                    <Text fz="xs" c="dimmed">
                      {RUN_COPY[run.status].meaning}
                    </Text>
                  </Stack>
                ),
              },
              {
                label: '目标',
                value: <TargetLink run={run} />,
              },
              {
                label: '线路进度',
                value: `选中 ${run.progress.channels_total} 条，已完成 ${run.progress.channels_finished} 条`,
              },
              {
                label: '尝试次数',
                value: `第 ${run.attempt} 次`,
              },
              {
                label: '请求 ID',
                value: <IdChip value={run.request_id} width={300} />,
              },
              {
                label: 'Idempotency-Key',
                value: <IdChip value={run.idempotency_key} width={300} />,
                hint: '同一个键不会重复入队',
              },
              ...(run.claimed_by
                ? [{ label: '执行者', value: <IdChip value={run.claimed_by} width={300} /> }]
                : []),
            ]}
            labelWidth={150}
          />
        </CardRow>
        <CardRow>
          <MetaLine gap="lg">
            <Fact label="创建">
              <Timestamp iso={run.created_at} />
            </Fact>
            {run.started_at && (
              <Fact label="开始">
                <Timestamp iso={run.started_at} />
              </Fact>
            )}
            {run.finished_at && (
              <Fact label="结束">
                <Timestamp iso={run.finished_at} />
              </Fact>
            )}
            {run.result && <Fact label="耗时">{run.result.meta.duration_ms} ms</Fact>}
          </MetaLine>
        </CardRow>
      </SectionCard>

      {run.last_error && <FailureSection error={run.last_error} />}

      {!isTerminalRun(run.status) && (
        <Caveat>这次执行还没有结束，本页会持续跟随它的状态，直到它结束为止。</Caveat>
      )}

      {run.result ? (
        <EnvelopeView envelope={run.result} />
      ) : (
        <MissingEnvelope run={run} />
      )}
    </Stack>
  )
}

function TargetLink({ run }: { run: Run }) {
  const path =
    run.resource.type === 'channel'
      ? `/channels/${run.resource.id}`
      : run.resource.type === 'view'
        ? `/views/${run.resource.id}`
        : undefined

  return (
    <Stack gap={2} align="flex-start">
      {path ? (
        <Anchor component={Link} to={path} fz="sm">
          {run.resource.id}
        </Anchor>
      ) : (
        <Text fz="sm">{run.resource.id}</Text>
      )}
      <Text fz="xs" c="dimmed">
        {run.resource.type}
      </Text>
    </Stack>
  )
}

/** `last_error` is why the Run itself ended, distinct from per-route errors. */
function FailureSection({ error }: { error: OmniError }) {
  const copy = describeError(error.code, error.retryable)
  return (
    <SectionCard title="失败原因">
      <CardRow>
        <Text fz="sm" fw={500}>
          {copy.text}
        </Text>
        {copy.hint && (
          <Text fz="xs" c="dimmed" mt={2}>
            {copy.hint}
          </Text>
        )}
        <MetaLine gap="md">
          {error.channel_id && <Fact label="Channel">{error.channel_id}</Fact>}
          {error.source && <Fact label="Source">{error.source}</Fact>}
          {error.provider && <Fact label="Provider">{error.provider}</Fact>}
          <Fact label="可重试">{error.retryable ? '是' : '否'}</Fact>
        </MetaLine>
        <Text fz={10} c="dimmed" ff="monospace" mt={4}>
          {error.code} {error.message}
        </Text>
      </CardRow>
    </SectionCard>
  )
}

/**
 * Why there is no Envelope. A probe never produces one; an unfinished Run has not
 * produced one yet. Saying which is true beats leaving the page looking broken.
 */
function MissingEnvelope({ run }: { run: Run }) {
  if (run.kind === 'channel_probe') {
    return (
      <SectionCard title="结果">
        <EmptyState
          title="Channel Probe 不产生 Envelope。"
          hint="它只验证这条线路能否连通，结论记录在该 Channel 的健康状态里，而不是一批 Items。"
          action={<PageLink to={`/channels/${run.resource.id}`}>查看这个 Channel 的检查结果</PageLink>}
        />
      </SectionCard>
    )
  }

  return (
    <SectionCard title="结果">
      {isTerminalRun(run.status) ? (
        <EmptyState
          title="这次执行没有留下结果。"
          hint="它在取数之前就结束了，所以没有 Coverage、Executions 或 Items 可以展示。"
        />
      ) : (
        <EmptyState title="结果还没有产生。" hint="执行结束后，这里会显示完整的 Envelope。" />
      )}
      {run.request && (
        <CardRow>
          <Text fz="xs" c="dimmed" mb={6}>
            计划执行的 Operation
          </Text>
          <JsonBlock value={run.request} maxHeight={280} />
        </CardRow>
      )}
    </SectionCard>
  )
}
