/**
 * ViewDetail — one View: its saved Operation, its current items, its Feed URLs.
 *
 * Four things here follow from what the live Backend actually does:
 *
 *   **`GET /v1/views/{id}` returns the same wrapper as the list**, not a bare
 *   `View`: `{view, status, snapshot, feed_urls}`. `useView` in queries.ts types it
 *   as `Fetched<View>`, which does not match the response, so this page reads the
 *   route through its own correctly-typed `useDetail` on the same query key — the
 *   key matters because `useRefreshView` invalidates it.
 *
 *   **The Operation is immutable.** `Service.ApplyView` compares the incoming
 *   Operation with the saved one and rejects any difference as `ErrConflict`,
 *   which arrives as `409 revision_conflict`. So the edit form changes the name and
 *   the enabled flag and replays the saved Operation verbatim; the page says the
 *   conditions are fixed instead of offering controls that cannot succeed.
 *
 *   **Snapshot metadata comes from the detail response**, which already carries
 *   `id`, `run_id`, `created_at` and `fresh_until`. `GET /v1/views/{id}/snapshot`
 *   returns the same four fields wrapped around the whole decoded Envelope, so
 *   calling it would re-download the entire materialized result to render a
 *   timestamp. `envelope` and `state_keys` are never decoded or shown.
 *
 *   **Item content is remote, untrusted HTML.** It is never injected. The excerpt
 *   is plain text extracted through an inert parsed document and truncated.
 */

import { useEffect, useState } from 'react'
import {
  Anchor,
  Box,
  Button,
  CopyButton,
  Group,
  Modal,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Tooltip,
  UnstyledButton,
} from '@mantine/core'
import {
  IconArrowLeft,
  IconCheck,
  IconCopy,
  IconPencil,
  IconRefresh,
  IconTrash,
} from '@tabler/icons-react'
import { Link, useNavigate, useParams } from 'react-router'
import {
  keys,
  READ_ONLY,
  useRefreshView,
  useRun,
  useUpdate,
  useView,
  useViewItems,
} from '../api/queries'
import { ProblemError } from '../api/client'
import type { Item, Operation, View } from '../api/types'
import { IdChip, RunBadge, Timestamp, ViewBadge } from '../components/display'
import {
  Caveat,
  CardRow,
  DemoModeNotice,
  EmptyState,
  ErrorAlert,
  Fact,
  FieldList,
  JsonBlock,
  LoadingRow,
  MetaLine,
  PageHeader,
  SectionCard,
} from '../components/layout'
import { DeleteViewDialog, DEDUPE_COPY, KIND_COPY } from './Views'

/** PUT /v1/views/{id}. Full replacement; the Operation must be replayed verbatim. */
interface ReplaceViewBody {
  id: string
  display_name: string
  operation: Operation
  enabled: boolean
}

/** True when the Backend refused a read because no Snapshot exists yet. */
function isSnapshotMissing(error: unknown): boolean {
  return error instanceof ProblemError && error.problem.code === 'snapshot_unavailable'
}

const FEED_FORMATS: { key: 'json' | 'rss' | 'atom'; label: string }[] = [
  { key: 'json', label: 'JSON Feed' },
  { key: 'rss', label: 'RSS' },
  { key: 'atom', label: 'Atom' },
]

export function ViewDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const detail = useView(id)
  const items = useViewItems(id)
  const refresh = useRefreshView()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const startedRun = useRun(refresh.data?.id, Boolean(refresh.data))

  // Once the Run this page started reaches a terminal status, the snapshot and
  // items it produced are the point of the refresh — re-read them.
  const finishedRunId =
    startedRun.data && ['complete', 'partial', 'failed', 'cancelled'].includes(startedRun.data.status)
      ? startedRun.data.id
      : null
  // Depends only on the Run id so it fires once per completed refresh, rather
  // than on every render that produces new refetch identities.
  useEffect(() => {
    if (!finishedRunId) return
    void detail.refetch()
    void items.refetch()
  }, [finishedRunId, detail.refetch, items.refetch])

  if (detail.isLoading) {
    return (
      <Stack gap="lg">
        <PageHeader title="View" />
        <LoadingRow />
      </Stack>
    )
  }

  if (detail.error || !detail.data) {
    return (
      <Stack gap="lg">
        <PageHeader
          title="View"
          actions={
            <Button
              variant="default"
              component={Link}
              to="/views"
              leftSection={<IconArrowLeft size={14} />}
            >
              返回列表
            </Button>
          }
        />
        <ErrorAlert error={detail.error} onRetry={() => void detail.refetch()} />
      </Stack>
    )
  }

  const { view, status, snapshot, feed_urls: feedUrls } = detail.data.data
  const etag = detail.data.etag
  const operation = view.operation
  const scope = operation.scope.channels ?? []

  return (
    <Stack gap="lg">
      <PageHeader
        title={view.display_name || view.id}
        description={<IdChip value={view.id} width={320} />}
        actions={
          <>
            <Button
              variant="default"
              component={Link}
              to="/views"
              leftSection={<IconArrowLeft size={14} />}
            >
              返回列表
            </Button>
            <Button
              variant="default"
              leftSection={<IconPencil size={14} />}
              onClick={() => setEditing(true)}
              disabled={READ_ONLY}
            >
              编辑
            </Button>
            <Button
              variant="default"
              color="red"
              leftSection={<IconTrash size={14} />}
              onClick={() => setDeleting(true)}
              disabled={READ_ONLY}
            >
              删除
            </Button>
            <Button
              leftSection={<IconRefresh size={14} />}
              onClick={() => refresh.mutate(view.id)}
              loading={refresh.isPending}
              disabled={READ_ONLY || !view.enabled}
            >
              刷新
            </Button>
          </>
        }
      />

      <DemoModeNotice />
      {refresh.error ? <ErrorAlert error={refresh.error} /> : null}

      <SectionCard title="当前状态">
        <CardRow>
          <Group gap="xs" wrap="wrap">
            <ViewBadge status={status} />
            {startedRun.data && <RunBadge status={startedRun.data.status} />}
            {!view.enabled && (
              <Text fz="xs" c="dimmed">
                已停用，不会刷新，也不对外分发。
              </Text>
            )}
          </Group>
          {startedRun.data && (
            <MetaLine gap="sm">
              <Fact label="本次 Run">
                <Anchor component={Link} to={`/runs/${startedRun.data.id}`} fz="xs">
                  查看执行详情
                </Anchor>
              </Fact>
              <Fact label="已完成线路">
                {startedRun.data.progress.channels_finished} / {startedRun.data.progress.channels_total}
              </Fact>
            </MetaLine>
          )}
        </CardRow>
        <CardRow>
          {snapshot ? (
            <FieldList
              fields={[
                { label: 'Snapshot', value: <IdChip value={snapshot.id} width={340} /> },
                {
                  label: '来自 Run',
                  value: (
                    <Anchor component={Link} to={`/runs/${snapshot.run_id}`} fz="sm">
                      {snapshot.run_id}
                    </Anchor>
                  ),
                },
                { label: '生成时间', value: <Timestamp iso={snapshot.created_at} /> },
                {
                  label: '新鲜期至',
                  value: <Timestamp iso={snapshot.fresh_until} />,
                  hint: '超过这个时间后内容仍可读，但标记为已过期',
                },
              ]}
            />
          ) : (
            <Text fz="sm" c="dimmed">
              还没有 Snapshot。刷新一次之后这里会显示它的生成时间与来源 Run。
            </Text>
          )}
        </CardRow>
      </SectionCard>

      <SectionCard title="Feed 地址">
        <CardRow>
          <Text fz="xs" c="dimmed">
            由 Backend 签发，可直接加进任意阅读器。停用这个 View 会让三个地址同时失效。
          </Text>
        </CardRow>
        {FEED_FORMATS.map((format) => (
          <CardRow key={format.key}>
            <Group justify="space-between" wrap="nowrap" gap="md">
              <Box style={{ minWidth: 0 }}>
                <Text fz="xs" c="dimmed">
                  {format.label}
                </Text>
                <Text fz="sm" ff="monospace" truncate>
                  {feedUrls[format.key]}
                </Text>
              </Box>
              <CopyButton value={feedUrls[format.key]} timeout={1400}>
                {({ copied, copy }) => (
                  <Tooltip label={copied ? '已复制' : '复制地址'} fz="xs">
                    <UnstyledButton
                      onClick={copy}
                      aria-label={copied ? '已复制' : `复制 ${format.label} 地址`}
                      style={{ display: 'grid', placeItems: 'center', width: 28, height: 28, flex: 'none' }}
                    >
                      {copied ? (
                        <IconCheck size={15} stroke={2.6} color="var(--mantine-color-teal-6)" />
                      ) : (
                        <IconCopy size={15} stroke={2} />
                      )}
                    </UnstyledButton>
                  </Tooltip>
                )}
              </CopyButton>
            </Group>
          </CardRow>
        ))}
      </SectionCard>

      <SectionCard title="保存的 Operation">
        <CardRow>
          <FieldList
            fields={[
              {
                label: '取数方式',
                value: KIND_COPY[operation.operation].label,
                hint: KIND_COPY[operation.operation].hint,
              },
              ...(operation.query
                ? [{ label: '搜索关键词', value: operation.query }]
                : []),
              ...(operation.target ? [{ label: '目标地址', value: operation.target }] : []),
              {
                label: 'Channel 范围',
                value:
                  scope.length > 0 ? (
                    <Stack gap={4}>
                      {scope.map((channelId) => (
                        <Anchor
                          key={channelId}
                          component={Link}
                          to={`/channels/${channelId}`}
                          fz="sm"
                        >
                          {channelId}
                        </Anchor>
                      ))}
                    </Stack>
                  ) : (
                    '未按 Channel 限定'
                  ),
              },
              { label: '条目上限', value: String(operation.limit) },
              {
                label: '去重',
                value: DEDUPE_COPY[operation.identity_dedupe].label,
                hint: DEDUPE_COPY[operation.identity_dedupe].hint,
              },
              {
                label: '路由策略',
                value: operation.route_policy.mode === 'auto' ? '自动选择线路' : operation.route_policy.mode,
                hint: operation.route_policy.allow_fallback ? '允许回退到备用线路' : '不回退',
              },
              { label: '执行时限', value: `${operation.deadline_ms} 毫秒` },
            ]}
          />
        </CardRow>
        <CardRow>
          <Text fz="xs" c="dimmed" mb={6}>
            以下是 Backend 保存的原始 Operation，也是这个 View 每次执行的确切契约。
          </Text>
          <JsonBlock value={operation} />
        </CardRow>
      </SectionCard>

      <SectionCard
        title="当前内容"
        count={items.data ? `${items.data.items.length} 条` : undefined}
      >
        {items.isLoading ? (
          <LoadingRow />
        ) : isSnapshotMissing(items.error) ? (
          // Not a failure: this View simply has not produced a Snapshot yet.
          <EmptyState
            title="还没有可读取的内容。"
            hint="这个 View 还没有成功刷新过。刷新一次之后，取到的条目会连同它们的来源出处显示在这里。"
          />
        ) : items.error ? (
          <CardRow>
            <ErrorAlert error={items.error} onRetry={() => void items.refetch()} />
          </CardRow>
        ) : !items.data || items.data.items.length === 0 ? (
          <EmptyState
            title="这个 Snapshot 里没有条目。"
            hint="刷新执行成功但没有取到内容时会是这样，通常说明来源在当前条件下没有匹配项。"
          />
        ) : (
          <>
            {items.data.stale && (
              <CardRow>
                <Caveat>
                  下面的内容来自已经超过新鲜期的 Snapshot。它们是真实取到的结果，不是错误，但可能不是来源现在的状态。刷新一次可以取到最新内容。
                </Caveat>
              </CardRow>
            )}
            <Table.ScrollContainer minWidth={720}>
              <Table verticalSpacing="sm" horizontalSpacing="md">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>标题</Table.Th>
                    <Table.Th>来源</Table.Th>
                    <Table.Th>发布时间</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {items.data.items.map((item) => (
                    <ItemRow key={item.id} item={item} />
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          </>
        )}
      </SectionCard>

      <EditViewModal
        view={view}
        etag={etag}
        opened={editing}
        onClose={() => setEditing(false)}
      />
      <DeleteViewDialog
        view={view}
        opened={deleting}
        onClose={() => setDeleting(false)}
        onDeleted={() => void navigate('/views')}
      />
    </Stack>
  )
}

function ItemRow({ item }: { item: Item }) {
  const excerpt = plainExcerpt(item)
  const authors = (item.authors ?? []).map((author) => author.name).filter(Boolean)

  return (
    <Table.Tr>
      <Table.Td style={{ maxWidth: 420 }}>
        <Anchor href={item.url} target="_blank" rel="noopener noreferrer" fz="sm" fw={500}>
          {item.title || item.url}
        </Anchor>
        {excerpt && (
          <Text fz="xs" c="dimmed" mt={4} lineClamp={2}>
            摘录：{excerpt}
          </Text>
        )}
        {authors.length > 0 && (
          <MetaLine gap="sm">
            <Fact label="作者">{authors.join('，')}</Fact>
          </MetaLine>
        )}
      </Table.Td>
      <Table.Td>
        <Stack gap={4}>
          {item.observations.map((observation, index) => (
            <Box key={`${observation.channel_id}-${index}`}>
              <Text fz="sm">{observation.source}</Text>
              <MetaLine gap="sm">
                <Fact label="Provider">{observation.provider}</Fact>
                <Fact label="Channel">
                  <Anchor component={Link} to={`/channels/${observation.channel_id}`} fz="xs">
                    {observation.channel_id}
                  </Anchor>
                </Fact>
              </MetaLine>
              {observation.endpoint && (
                <MetaLine gap="sm">
                  <Fact label="Endpoint">
                    <Text span fz="xs" ff="monospace" c="dimmed">
                      {observation.endpoint}
                    </Text>
                  </Fact>
                </MetaLine>
              )}
            </Box>
          ))}
        </Stack>
      </Table.Td>
      <Table.Td>
        {item.published_at ? (
          <Timestamp iso={item.published_at} />
        ) : (
          <Text fz="xs" c="dimmed">
            来源未提供
          </Text>
        )}
      </Table.Td>
    </Table.Tr>
  )
}

/**
 * A short plain-text excerpt from an Item's content.
 *
 * `content.html` is remote HTML from an arbitrary feed and is never injected. When
 * only HTML exists, it is parsed into an inert document — `DOMParser` neither runs
 * scripts nor loads subresources, and the result is never attached to this
 * document — and only its text is read out.
 */
function plainExcerpt(item: Item, maxLength = 160): string | null {
  const content = item.content
  if (!content) return null

  let text = content.text?.trim() ?? ''
  if (!text && content.html) {
    try {
      const parsed = new DOMParser().parseFromString(content.html, 'text/html')
      text = (parsed.body.textContent ?? '').trim()
    } catch {
      return null
    }
  }

  const collapsed = text.replace(/\s+/g, ' ').trim()
  if (!collapsed) return null
  return collapsed.length > maxLength ? `${collapsed.slice(0, maxLength)}…` : collapsed
}

/**
 * Editing, as a full PUT carrying the ETag read with the detail.
 *
 * The Operation is not editable — the Backend rejects a changed one as
 * `revision_conflict` — so it is resent exactly as it was read. A real
 * `409 revision_conflict` from a concurrent edit surfaces through ErrorAlert and
 * is never retried or overwritten: the user re-reads and decides.
 */
function EditViewModal({
  view,
  etag,
  opened,
  onClose,
}: {
  view: View
  etag: string | null
  opened: boolean
  onClose: () => void
}) {
  const update = useUpdate<ReplaceViewBody, View>(
    (id) => `/v1/views/${id}`,
    [keys.views, keys.view(view.id)],
  )
  const [displayName, setDisplayName] = useState(view.display_name)
  const [enabled, setEnabled] = useState(view.enabled)

  // Re-seed from the freshly read resource each time the dialog opens, so an
  // abandoned edit cannot leak into the next one.
  useEffect(() => {
    if (!opened) return
    setDisplayName(view.display_name)
    setEnabled(view.enabled)
    update.reset()
  }, [opened, view.display_name, view.enabled, update.reset])

  const submit = async () => {
    if (!etag) return
    try {
      await update.mutateAsync({
        id: view.id,
        etag,
        body: {
          id: view.id,
          display_name: displayName.trim(),
          operation: view.operation,
          enabled,
        },
      })
      onClose()
    } catch {
      // Kept open so a conflict stays visible next to the fields.
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title="编辑 View" size="md" centered>
      <Stack gap="sm">
        <TextInput
          label="显示名称"
          value={displayName}
          onChange={(event) => setDisplayName(event.currentTarget.value)}
          required
        />
        <Switch
          label="启用"
          description="停用后不再刷新，三个 Feed 地址同时停止对外分发。"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        <Text fz="xs" c="dimmed">
          取数条件在创建时就已固定，Backend 不接受修改。要换 Channel 范围、取数方式或条目上限，请新建一个 View。
        </Text>

        {!etag && (
          <Text fz="xs" c="red">
            没有读到 ETag，无法安全提交。请先刷新页面。
          </Text>
        )}
        {update.error ? <ErrorAlert error={update.error} /> : null}

        <Group justify="flex-end" gap="xs" mt="xs">
          <Button variant="default" onClick={onClose}>
            取消
          </Button>
          <Button
            onClick={() => void submit()}
            loading={update.isPending}
            disabled={!etag || displayName.trim() === ''}
          >
            保存
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
