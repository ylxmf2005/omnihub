/**
 * Envelope rendering, shared by RunDetail and Workbench.
 *
 * The whole point of this file is that `coverage` and `errors` are two different
 * facts and are never collapsed into one verdict:
 *
 *   - `coverage[].truncated` / `exhaustive: false` says the *window* was cut
 *     short — by `limit`, or by how much history the upstream keeps. Nothing
 *     failed. The live instance has a `partial` view_refresh whose `errors` array
 *     is empty and whose coverage reports `examined: 50, returned: 20`.
 *   - `errors[]` says a route actually failed.
 *
 * A `partial` therefore reads as "results arrived, with gaps", and when there are
 * no errors the UI says so explicitly, because an earlier version of this page
 * told the user a route had failed when only the limit had bitten.
 *
 * Items carry `content.html`: remote, untrusted markup from arbitrary feeds. It is
 * never injected. `plainExcerpt` reads text out of an inert parsed document, so no
 * remote node ever reaches the live DOM.
 */

import { Anchor, Badge, Box, Group, Stack, Table, Text } from '@mantine/core'
import type {
  CoverageEntry,
  Envelope,
  ExecutionRecord,
  Item,
  ItemContent,
  ItemObservation,
  OmniError,
} from '../api/types'
import { IdChip, StateBadge, Timestamp } from './display'
import {
  CardRow,
  Caveat,
  EmptyState,
  Fact,
  FieldList,
  JsonBlock,
  MetaLine,
  SectionCard,
} from './layout'
import { LIMITATION_COPY, describeError } from '../domain/vocabulary'

// ---------------------------------------------------------------------------
// Page-local copy. Machine enums never reach the reader as-is.
// ---------------------------------------------------------------------------

export const OPERATION_COPY: Record<string, string> = {
  search: '搜索',
  latest: '最新',
  fetch: '抓取',
}

const EXECUTION_STATUS_COPY: Record<string, { label: string; tone: 'ok' | 'bad' | 'idle'; meaning: string }> = {
  completed: { label: '完成', tone: 'ok', meaning: '这条线路取数成功。' },
  failed: { label: '失败', tone: 'bad', meaning: '这条线路没有取到结果。' },
  skipped: { label: '跳过', tone: 'idle', meaning: '这条线路本次没有被执行。' },
}

const SELECTION_COPY: Record<string, string> = {
  primary: '主线路',
  fallback: '备用线路',
  candidate: '候选线路',
}

/** Why an execution was skipped. Real values observed on the live instance. */
const SKIP_REASON_COPY: Record<string, string> = {
  outside_channel_scope: '不在这次请求指定的 Channel 范围内',
  capability_unsupported: '这条线路不支持该 Operation',
}

const CONTINUATION_COPY: Record<string, string> = {
  none: '这次结果没有后续分页',
  cursor: '可以用游标继续取下一页',
}

// ---------------------------------------------------------------------------
// Derived facts
// ---------------------------------------------------------------------------

export interface EnvelopeSummary {
  examined: number
  returned: number
  /** Coverage rows whose window was cut short, or whose completeness is unknown. */
  gaps: CoverageEntry[]
  /** Route failures. Deliberately counted separately from `gaps`. */
  failures: number
}

export function summarizeEnvelope(envelope: Envelope): EnvelopeSummary {
  return {
    examined: envelope.coverage.reduce((sum, entry) => sum + entry.examined, 0),
    returned: envelope.coverage.reduce((sum, entry) => sum + entry.returned, 0),
    gaps: envelope.coverage.filter((entry) => entry.truncated || !entry.exhaustive),
    failures: envelope.errors.length,
  }
}

/**
 * Plain text from an Item, for an excerpt.
 *
 * `DOMParser` builds an inert document: scripts do not run, subresources are not
 * fetched, and nothing is attached to the page. Reading `textContent` from it is
 * the safe way to summarize remote HTML — the alternative, `dangerouslySetInnerHTML`,
 * would hand arbitrary feed markup script execution in the user's local instance.
 */
export function plainExcerpt(content: ItemContent | undefined, max = 200): string | undefined {
  if (!content) return undefined
  const source =
    content.text ??
    (content.html ? new DOMParser().parseFromString(content.html, 'text/html').body.textContent : undefined)
  const text = source?.replace(/\s+/g, ' ').trim()
  if (!text) return undefined
  return text.length > max ? `${text.slice(0, max)}…` : text
}

// ---------------------------------------------------------------------------
// Envelope
// ---------------------------------------------------------------------------

export function EnvelopeView({ envelope }: { envelope: Envelope }) {
  return (
    <Stack gap="lg">
      <Outcome envelope={envelope} />
      {envelope.errors.length > 0 && <ErrorsSection errors={envelope.errors} />}
      <CoverageSection coverage={envelope.coverage} />
      <ExecutionsSection executions={envelope.executions} />
      <ItemsSection items={envelope.items} />
      <ExecutionInfoSection envelope={envelope} />
    </Stack>
  )
}

/**
 * The verdict line. Says what the status means, then reports coverage and route
 * failures as two separate statements — including the explicit "no route failed"
 * when a `partial` is purely a scope gap.
 */
function Outcome({ envelope }: { envelope: Envelope }) {
  const { examined, returned, gaps, failures } = summarizeEnvelope(envelope)

  if (envelope.status === 'complete' && gaps.length === 0) return null

  if (envelope.status === 'failed') {
    return (
      <Caveat>
        这次执行没有取到任何结果。
        {failures > 0
          ? `${failures} 条线路失败，原因见下方。`
          : '也没有记录到线路错误，可能在选路阶段就没有可执行的线路。'}
      </Caveat>
    )
  }

  return (
    <Caveat>
      <Stack gap={4}>
        <Text fz="xs">
          {envelope.status === 'partial'
            ? `取到了 ${envelope.items.length} 条结果，但覆盖范围不完整。`
            : `取到了 ${envelope.items.length} 条结果。`}
        </Text>
        {gaps.length > 0 && (
          <Text fz="xs">
            {gaps.length} 个范围没有取完：本次检查了 {examined} 条、返回 {returned} 条。
            {gaps.some((entry) => entry.truncated)
              ? '窗口被 limit 或上游保留策略截断，往前的内容需要另一次执行才能取到。'
              : '上游没有给出足以判断是否取完的信息。'}
          </Text>
        )}
        <Text fz="xs">
          {failures > 0
            ? `另有 ${failures} 条线路失败，与覆盖范围无关，原因见下方。`
            : '没有任何线路失败。'}
        </Text>
      </Stack>
    </Caveat>
  )
}

// ---------------------------------------------------------------------------
// Coverage — the section that explains a `partial`
// ---------------------------------------------------------------------------

function CoverageSection({ coverage }: { coverage: CoverageEntry[] }) {
  return (
    <SectionCard
      title="Coverage"
      count={coverage.length > 0 ? `${coverage.length} 个范围` : undefined}
    >
      {coverage.length === 0 ? (
        <EmptyState
          title="没有覆盖范围记录。"
          hint="没有线路进入取数阶段，所以没有可报告的范围。"
        />
      ) : (
        <Table.ScrollContainer minWidth={760}>
          <Table verticalSpacing="sm" horizontalSpacing="md">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Channel</Table.Th>
                <Table.Th>范围</Table.Th>
                <Table.Th>检查过</Table.Th>
                <Table.Th>返回</Table.Th>
                <Table.Th>完整性</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {coverage.map((entry) => (
                <Table.Tr key={`${entry.channel_id}-${entry.scope}`}>
                  <Table.Td>
                    <Text fz="sm">{entry.channel_id}</Text>
                    <Text fz="xs" c="dimmed">
                      {entry.source}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text fz="xs" ff="monospace" c="dimmed">
                      {entry.scope}
                    </Text>
                    {entry.from && entry.to && (
                      <MetaLine gap="sm">
                        <Fact label="从">
                          <Timestamp iso={entry.from} />
                        </Fact>
                        <Fact label="到">
                          <Timestamp iso={entry.to} />
                        </Fact>
                      </MetaLine>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Text fz="sm" ff="monospace">
                      {entry.examined}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text fz="sm" ff="monospace">
                      {entry.returned}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={6} align="flex-start">
                      <CompletenessBadge entry={entry} />
                      <LimitationList codes={entry.limitations} />
                    </Stack>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </SectionCard>
  )
}

/** Completeness is a scope statement, never a failure statement. */
function CompletenessBadge({ entry }: { entry: CoverageEntry }) {
  if (entry.truncated) {
    return (
      <StateBadge
        label="窗口被截断"
        tone="warn"
        code="truncated"
        meaning="受 limit 或上游保留策略限制，这次没有取完可取的范围。这不是失败。"
      />
    )
  }
  if (entry.exhaustive) {
    return (
      <StateBadge
        label="范围完整"
        tone="ok"
        code="exhaustive"
        meaning="这次覆盖了该范围内的全部内容。"
      />
    )
  }
  return (
    <StateBadge
      label="完整性未知"
      tone="idle"
      code="exhaustive=false"
      meaning="上游没有给出足以判断范围是否取完的信息。"
    />
  )
}

/**
 * Named caveats. A code we have copy for is shown as a sentence; one we do not is
 * shown raw and dimmed rather than guessed at or hidden.
 */
function LimitationList({ codes }: { codes?: string[] }) {
  if (!codes || codes.length === 0) return null
  return (
    <Stack gap={2}>
      {codes.map((code) =>
        LIMITATION_COPY[code] ? (
          <Text key={code} fz="xs" c="dimmed">
            {LIMITATION_COPY[code]}
          </Text>
        ) : (
          <Text key={code} fz={10} c="dimmed" ff="monospace">
            {code}
          </Text>
        ),
      )}
    </Stack>
  )
}

// ---------------------------------------------------------------------------
// Executions
// ---------------------------------------------------------------------------

function ExecutionsSection({ executions }: { executions: ExecutionRecord[] }) {
  const attempted = executions.filter((record) => record.status !== 'skipped')

  return (
    <SectionCard
      title="Executions"
      count={
        executions.length > 0
          ? `${attempted.length} 条执行，共 ${executions.length} 条候选`
          : undefined
      }
    >
      {executions.length === 0 ? (
        <EmptyState title="没有线路执行记录。" />
      ) : (
        <Table.ScrollContainer minWidth={980}>
          <Table verticalSpacing="sm" horizontalSpacing="md">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Channel</Table.Th>
                <Table.Th>Provider</Table.Th>
                <Table.Th>选路</Table.Th>
                <Table.Th>Egress</Table.Th>
                <Table.Th>状态</Table.Th>
                <Table.Th>耗时</Table.Th>
                <Table.Th>检查过</Table.Th>
                <Table.Th>返回</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {executions.map((record) => (
                <ExecutionRow key={`${record.channel_id}-${record.route_template_id}`} record={record} />
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </SectionCard>
  )
}

function ExecutionRow({ record }: { record: ExecutionRecord }) {
  const status = EXECUTION_STATUS_COPY[record.status]
  const skipped = record.status === 'skipped'
  const error = record.error ? describeError(record.error.code, record.error.retryable) : undefined

  return (
    <Table.Tr>
      <Table.Td>
        <Text fz="sm">{record.channel_id}</Text>
        <Text fz="xs" c="dimmed">
          {record.source}
        </Text>
      </Table.Td>
      <Table.Td>
        <Text fz="sm">{record.provider}</Text>
        <Text fz="xs" c="dimmed">
          {OPERATION_COPY[record.capability] ?? record.capability}
        </Text>
      </Table.Td>
      {/* `record.limitations` is deliberately not shown: for an executed Channel it
          repeats the Coverage row verbatim, and for a skipped one it describes a
          route that contributed nothing to this result. */}
      <Table.Td>
        <Text fz="sm">{SELECTION_COPY[record.selection] ?? record.selection}</Text>
      </Table.Td>
      <Table.Td>
        <EgressCell record={record} />
      </Table.Td>
      <Table.Td>
        <Stack gap={6} align="flex-start">
          {status ? (
            <StateBadge
              label={status.label}
              tone={status.tone}
              code={record.status}
              meaning={status.meaning}
            />
          ) : (
            <Text fz="xs" ff="monospace" c="dimmed">
              {record.status}
            </Text>
          )}
          {record.reason && (
            <Text fz="xs" c="dimmed">
              {SKIP_REASON_COPY[record.reason] ?? record.reason}
            </Text>
          )}
          {error && (
            <Box>
              <Text fz="xs">{error.text}</Text>
              {record.error && (
                <Text fz={10} c="dimmed" ff="monospace">
                  {record.error.code}
                </Text>
              )}
            </Box>
          )}
        </Stack>
      </Table.Td>
      <Table.Td>
        {skipped ? (
          <Text fz="xs" c="dimmed">
            未执行
          </Text>
        ) : (
          <Text fz="sm" ff="monospace">
            {record.duration_ms} ms
          </Text>
        )}
      </Table.Td>
      <Table.Td>
        {skipped ? (
          <Text fz="xs" c="dimmed">
            未执行
          </Text>
        ) : (
          <Text fz="sm" ff="monospace">
            {record.examined ?? 0}
          </Text>
        )}
      </Table.Td>
      <Table.Td>
        {skipped ? (
          <Text fz="xs" c="dimmed">
            未执行
          </Text>
        ) : (
          <Text fz="sm" ff="monospace">
            {record.returned ?? 0}
          </Text>
        )}
      </Table.Td>
    </Table.Tr>
  )
}

/**
 * Which Egress the request actually left through.
 *
 * This is evidence, not decoration: it is the only way to see that a Channel
 * configured to go through a proxy really did, rather than having quietly fallen
 * back to a direct connection. So `proxied` is stated in words, and an execution
 * that reports no Egress at all says so instead of rendering blank.
 */
function EgressCell({ record }: { record: ExecutionRecord }) {
  if (!record.egress) {
    // Live data carries `egress` only on a route that actually ran, so a skipped
    // one has nothing to report rather than a missing record.
    return (
      <Text fz="xs" c="dimmed">
        {record.status === 'skipped' ? '未执行' : '未记录'}
      </Text>
    )
  }
  return (
    <Stack gap={2}>
      <Text fz="sm">{record.egress.proxied ? '经代理' : '直连'}</Text>
      <Text fz="xs" c="dimmed">
        {record.egress.profile_id}
      </Text>
      <Text fz={10} c="dimmed" ff="monospace">
        {record.egress.mode}
      </Text>
    </Stack>
  )
}

// ---------------------------------------------------------------------------
// Route failures
// ---------------------------------------------------------------------------

function ErrorsSection({ errors }: { errors: OmniError[] }) {
  return (
    <SectionCard title="线路错误" count={`${errors.length} 条`}>
      {errors.map((error, index) => {
        const copy = describeError(error.code, error.retryable)
        return (
          <CardRow key={`${error.code}-${error.channel_id ?? index}`}>
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
              {error.provider && <Fact label="Provider">{error.provider}</Fact>}
              <Fact label="可重试">{error.retryable ? '是' : '否'}</Fact>
            </MetaLine>
            <Text fz={10} c="dimmed" ff="monospace" mt={4}>
              {error.code} {error.message}
            </Text>
          </CardRow>
        )
      })}
    </SectionCard>
  )
}

// ---------------------------------------------------------------------------
// Items
// ---------------------------------------------------------------------------

function ItemsSection({ items }: { items: Item[] }) {
  return (
    <SectionCard title="Items" count={items.length > 0 ? `${items.length} 条` : undefined}>
      {items.length === 0 ? (
        <EmptyState
          title="这次执行没有返回内容。"
          hint="上方的 Coverage 与线路错误说明了原因。"
        />
      ) : (
        items.map((item) => <ItemRow key={item.id} item={item} />)
      )}
    </SectionCard>
  )
}

function ItemRow({ item }: { item: Item }) {
  const excerpt = plainExcerpt(item.content)
  const observation: ItemObservation | undefined = item.observations[0]
  const authors = (item.authors ?? []).map((author) => author.name).filter(Boolean)

  return (
    <CardRow>
      <Anchor href={item.url} target="_blank" rel="noreferrer noopener" fz="sm" fw={500}>
        {item.title || item.url}
      </Anchor>
      {excerpt && (
        <Text fz="xs" c="dimmed" mt={4}>
          摘录：{excerpt}
        </Text>
      )}
      <MetaLine gap="md">
        {item.published_at && (
          <Fact label="发布">
            <Timestamp iso={item.published_at} relative />
          </Fact>
        )}
        {authors.length > 0 && <Fact label="作者">{authors.join('，')}</Fact>}
        {observation && <Fact label="来自">{observation.channel_id}</Fact>}
        {observation?.retrieved_at && (
          <Fact label="取得">
            <Timestamp iso={observation.retrieved_at} relative />
          </Fact>
        )}
      </MetaLine>
      {observation?.endpoint && (
        <Text fz={10} c="dimmed" ff="monospace" mt={4}>
          {observation.endpoint}
        </Text>
      )}
    </CardRow>
  )
}

// ---------------------------------------------------------------------------
// Selection, continuation, timing, and the exact Operation
// ---------------------------------------------------------------------------

function ExecutionInfoSection({ envelope }: { envelope: Envelope }) {
  const continuation = envelope.continuation

  return (
    <SectionCard title="执行信息">
      <CardRow>
        <FieldList
          fields={[
            {
              label: '选中的 Channel',
              value: (
                <Group gap="xs" wrap="wrap">
                  {envelope.selected_channel_ids.length === 0 ? (
                    <Text fz="sm" c="dimmed">
                      没有线路被选中
                    </Text>
                  ) : (
                    envelope.selected_channel_ids.map((id) => (
                      <Badge key={id} variant="default" styles={{ label: { fontWeight: 400, textTransform: 'none' } }}>
                        {id}
                      </Badge>
                    ))
                  )}
                </Group>
              ),
            },
            {
              label: '耗时',
              value: `${envelope.meta.duration_ms} ms`,
            },
            {
              label: '起止时间',
              value: (
                <MetaLine gap="md">
                  <Fact label="开始">
                    <Timestamp iso={envelope.meta.started_at} />
                  </Fact>
                  <Fact label="结束">
                    <Timestamp iso={envelope.meta.finished_at} />
                  </Fact>
                </MetaLine>
              ),
            },
            {
              label: '结果计数',
              value: `${envelope.meta.result_count}`,
              hint: 'Backend 记录的条数',
            },
            {
              label: '后续分页',
              value: (
                <Stack gap={2}>
                  <Text fz="sm">
                    {CONTINUATION_COPY[continuation.mode] ?? continuation.mode}
                  </Text>
                  <LimitationList codes={continuation.limitations} />
                </Stack>
              ),
            },
            {
              label: '请求 ID',
              value: <IdChip value={envelope.request_id} width={280} />,
            },
          ]}
        />
      </CardRow>
      <CardRow>
        <Text fz="xs" c="dimmed" mb={6}>
          本次执行的 Operation
        </Text>
        <JsonBlock value={envelope.request} maxHeight={280} />
      </CardRow>
    </SectionCard>
  )
}
