/**
 * Views — the list, plus creation, refresh and deletion.
 *
 * Three behaviours were read off the live Backend and shape this page:
 *
 *   **A View's Operation is immutable.** `Service.ApplyView` compares the
 *   incoming Operation against the saved one and refuses any difference with
 *   `ErrConflict`, which reaches the browser as `409 revision_conflict`. So this
 *   page is the only place an Operation is ever composed, and the form says so —
 *   a later edit can change the name and the enabled flag, nothing else.
 *
 *   **The offered Operation kinds come from the selected Channels.** Each
 *   RouteTemplate declares which kinds it can execute. Only the kinds *every*
 *   selected Channel supports are offered, because creating a `fetch` View over a
 *   feed-only Channel is accepted at write time and then fails on every refresh —
 *   a worse outcome than not offering it.
 *
 *   **A refresh is a Run, not a boolean.** Refreshing returns 202 with a Run; the
 *   row polls it to a terminal status. `partial` is a success with coverage gaps,
 *   so it is reported as its own outcome rather than as a failure.
 */

import { useState } from 'react'
import {
  Anchor,
  Badge,
  Box,
  Button,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPlus, IconRefresh, IconTrash } from '@tabler/icons-react'
import { Link } from 'react-router'
import {
  keys,
  READ_ONLY,
  useChannels,
  useCreate,
  useDelete,
  useRefreshView,
  useRouteTemplates,
  useRun,
  useViews,
} from '../api/queries'
import { get } from '../api/client'
import type { OmniError, OperationKind, View, ViewListEntry } from '../api/types'
import { IdChip, RunBadge, Timestamp, ViewBadge } from '../components/display'
import {
  ConfirmDialog,
  DemoModeNotice,
  EmptyState,
  ErrorAlert,
  Fact,
  LoadingRow,
  MetaLine,
  PageHeader,
  SectionCard,
} from '../components/layout'
import { describeError } from '../domain/vocabulary'

/** What each Operation kind does, in the terms the create form needs. */
export const KIND_COPY: Record<OperationKind, { label: string; hint: string }> = {
  latest: { label: '取最新', hint: '每次刷新取所选 Channel 的最新内容。' },
  search: { label: '按关键词搜索', hint: '每次刷新在所选 Channel 内搜索一个固定关键词。' },
  fetch: { label: '抓取指定地址', hint: '每次刷新抓取一个固定的目标地址。' },
}

export const DEDUPE_COPY: Record<'exact' | 'none', { label: string; hint: string }> = {
  exact: { label: '按身份去重', hint: '同一条内容从多个 Channel 取到时只保留一条。' },
  none: { label: '不去重', hint: '保留每个 Channel 各自返回的条目。' },
}

/**
 * POST /v1/views body.
 *
 * Two field names differ from `Operation` in types.ts, both verified against the
 * live schema: the kind lives in a field named `operation` (naming it `kind`
 * fails as `400 invalid_json`), and a `fetch` target is `target` — sending `url`
 * is rejected the same way.
 */
interface ViewOperationBody {
  schema_version: '1.0'
  operation: OperationKind
  scope: { channels: string[] }
  route_policy: { mode: 'auto'; aggregate: boolean; allow_fallback: boolean }
  limit: number
  time_range?: Record<string, never>
	constraints?: Record<string, never>
	sort?: 'relevance' | 'newest'
  identity_dedupe: 'exact' | 'none'
  similarity_grouping: 'off'
  deadline_ms: number
  query?: string
  target?: string
}

interface CreateViewBody {
  id: string
  display_name: string
  operation: ViewOperationBody
  enabled: boolean
}

/**
 * `last_failure` is the failed refresh Run, so the error is one level in at
 * `last_error`. The Run id is worth surfacing: it is the only route to the
 * per-channel detail of why the refresh failed.
 */
function lastFailureOf(entry: ViewListEntry): { runId?: string; error?: OmniError } | null {
  const failure = entry.last_failure
  if (!failure) return null
  return { runId: failure.id, error: failure.last_error }
}

export function Views() {
  const views = useViews()
  const [creating, setCreating] = useState(false)

  const list = views.data ?? []

  return (
    <Stack gap="lg">
      <PageHeader
        title="Views"
        description="每个 View 固定一条取数条件；刷新一次就生成一份 Snapshot，并对外提供可订阅的 Feed 地址。"
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              onClick={() => void views.refetch()}
              loading={views.isFetching}
            >
              刷新列表
            </Button>
            <Button
              leftSection={<IconPlus size={14} />}
              onClick={() => setCreating(true)}
              disabled={READ_ONLY}
            >
              新建 View
            </Button>
          </>
        }
      />

      <DemoModeNotice />
      {views.error ? <ErrorAlert error={views.error} onRetry={() => void views.refetch()} /> : null}

      <SectionCard title="已保存" count={list.length > 0 ? `${list.length} 个` : undefined}>
        {views.isLoading ? (
          <LoadingRow />
        ) : list.length === 0 ? (
          <EmptyState
            title="还没有 View。"
            hint="View 把一次取数固定下来：选好 Channel 和取数方式，之后每次刷新都按同样的条件执行，结果可以直接当 Feed 订阅。"
            action={
              <Button
                leftSection={<IconPlus size={14} />}
                onClick={() => setCreating(true)}
                disabled={READ_ONLY}
              >
                新建 View
              </Button>
            }
          />
        ) : (
          <Table.ScrollContainer minWidth={780}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>名称</Table.Th>
                  <Table.Th>取数方式</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {list.map((entry) => (
                  <ViewRow key={entry.view.id} entry={entry} />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <CreateViewModal opened={creating} onClose={() => setCreating(false)} />
    </Stack>
  )
}

function ViewRow({ entry }: { entry: ViewListEntry }) {
  const refresh = useRefreshView()
  const [deleting, setDeleting] = useState(false)

  // Follow the Run this refresh created until it settles, so the row reports the
  // real outcome rather than just "request accepted".
  const startedRun = useRun(refresh.data?.id, Boolean(refresh.data))

  // `active_run` arrives with the list, so a refresh started from the CLI or
  // another tab shows up here without cross-referencing the Runs list.
  const runInFlight = startedRun.data ?? entry.active_run ?? null
  const { view, status, snapshot } = entry
  const failure = lastFailureOf(entry)
  const scope = view.operation.scope.channels ?? []

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <Anchor component={Link} to={`/views/${view.id}`} fz="sm" fw={500}>
            {view.display_name || view.id}
          </Anchor>
          <Box mt={2}>
            <IdChip value={view.id} width={220} />
          </Box>
        </Table.Td>
        <Table.Td>
          <Text fz="sm">{KIND_COPY[view.operation.operation].label}</Text>
          <MetaLine gap="sm">
            <Fact label="范围">
              {scope.length > 0 ? `${scope.length} 个 Channel` : '未按 Channel 限定'}
            </Fact>
            <Fact label="上限">{view.operation.limit}</Fact>
          </MetaLine>
        </Table.Td>
        <Table.Td>
          <Group gap="xs" wrap="wrap">
            <ViewBadge status={status} />
            {!view.enabled && (
              <Badge variant="default" size="xs" styles={{ label: { fontWeight: 400 } }}>
                已停用
              </Badge>
            )}
            {runInFlight && <RunBadge status={runInFlight.status} />}
          </Group>
          {snapshot && (
            <MetaLine gap="sm">
              <Fact label="上次刷新">
                <Timestamp iso={snapshot.created_at} relative />
              </Fact>
              <Fact label="新鲜期至">
                <Timestamp iso={snapshot.fresh_until} />
              </Fact>
            </MetaLine>
          )}
          {failure?.error && (
            <MetaLine gap="sm">
              <Fact label="上次失败">
                {describeError(failure.error.code, failure.error.retryable).text}
              </Fact>
              {failure.runId && (
                <Anchor component={Link} to={`/runs/${failure.runId}`} fz="xs">
                  查看失败详情
                </Anchor>
              )}
            </MetaLine>
          )}
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="nowrap" justify="flex-end">
            <Button
              variant="default"
              size="compact-sm"
              leftSection={<IconRefresh size={13} />}
              disabled={READ_ONLY || !view.enabled}
              loading={refresh.isPending}
              onClick={() => refresh.mutate(view.id)}
            >
              刷新
            </Button>
            <Button
              variant="subtle"
              color="red"
              size="compact-sm"
              px={8}
              disabled={READ_ONLY}
              onClick={() => setDeleting(true)}
              aria-label={`删除 ${view.display_name || view.id}`}
            >
              <IconTrash size={14} />
            </Button>
          </Group>
        </Table.Td>
      </Table.Tr>

      {refresh.error ? (
        <Table.Tr>
          <Table.Td colSpan={4}>
            <ErrorAlert error={refresh.error} />
          </Table.Td>
        </Table.Tr>
      ) : null}

      <DeleteViewDialog view={view} opened={deleting} onClose={() => setDeleting(false)} />
    </>
  )
}

/**
 * Deletion in two steps: read the detail route to capture its strong ETag, hold
 * it across the confirmation, then send that exact validator.
 *
 * The Backend counts the View's Runs and current Snapshot before deleting and
 * answers `resource_in_use` if any exist, so a View that has ever been refreshed
 * cannot be removed. That refusal is surfaced as-is; the consequence copy says so
 * up front rather than letting the user discover it from an error.
 */
export function DeleteViewDialog({
  view,
  opened,
  onClose,
  onDeleted,
}: {
  view: View
  opened: boolean
  onClose: () => void
  onDeleted?: () => void
}) {
  const remove = useDelete((id) => `/v1/views/${id}`, [keys.views])
  const [etagError, setEtagError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setEtagError(null)
    try {
      const current = await get<unknown>(`/v1/views/${view.id}`)
      if (!current.etag) {
        throw new Error('Backend 没有返回 ETag，无法安全删除')
      }
      await remove.mutateAsync({ id: view.id, etag: current.etag })
      onClose()
      onDeleted?.()
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
      title="删除 View"
      target={view.display_name || view.id}
      consequence="删除后它的 Feed 地址立即失效。已经刷新过的 View 仍被 Run 与 Snapshot 引用，Backend 会拒绝删除并告知你，不会连带清除那些执行记录。"
      loading={pending}
      error={etagError ?? remove.error}
    />
  )
}

/**
 * Creation form.
 *
 * This is the only place an Operation can ever be written, so the form covers the
 * whole contract it needs to fix: kind, Channel scope, limit and dedupe. Route
 * policy stays `auto` — the live schema types `prefer`/`only`/`exclude` as lists
 * of `{kind, id}` objects, and exposing them would make this a routing editor
 * rather than a View form.
 */
function CreateViewModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate<CreateViewBody, View>('/v1/views', [keys.views])
  const channels = useChannels()
  const templates = useRouteTemplates()

  const [id, setId] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [channelIds, setChannelIds] = useState<string[]>([])
  const [kind, setKind] = useState<OperationKind>('latest')
  const [query, setQuery] = useState('')
  const [target, setTarget] = useState('')
  const [limit, setLimit] = useState<number>(20)
  const [dedupe, setDedupe] = useState<'exact' | 'none'>('exact')
  const [enabled, setEnabled] = useState(true)

  const capabilityOf = new Map(
    (templates.data ?? []).map((template) => [template.route_template_id, template.capabilities]),
  )

  const allKinds: OperationKind[] = ['latest', 'search', 'fetch']
  const selected = (channels.data ?? []).filter((channel) => channelIds.includes(channel.id))
  // With nothing selected there is no evidence to narrow by, so all kinds stay
  // offered; once Channels are chosen, only what all of them can execute.
  const availableKinds =
    selected.length === 0
      ? allKinds
      : allKinds.filter((candidate) =>
          selected.every((channel) =>
            (capabilityOf.get(channel.route_template_id) ?? []).includes(candidate),
          ),
        )

  const effectiveKind = availableKinds.includes(kind) ? kind : (availableKinds[0] ?? null)

  const reset = () => {
    setId('')
    setDisplayName('')
    setChannelIds([])
    setKind('latest')
    setQuery('')
    setTarget('')
    setLimit(20)
    setDedupe('exact')
    setEnabled(true)
    create.reset()
  }

  const submit = async () => {
    if (!effectiveKind) return
    try {
      await create.mutateAsync({
        id: id.trim(),
        display_name: displayName.trim(),
        enabled,
        operation: {
          schema_version: '1.0',
          operation: effectiveKind,
          scope: { channels: channelIds },
          route_policy: { mode: 'auto', aggregate: false, allow_fallback: false },
          limit,
		  ...(effectiveKind === 'latest' ? { time_range: {} } : {}),
          identity_dedupe: dedupe,
          similarity_grouping: 'off',
          deadline_ms: 30_000,
		  ...(effectiveKind === 'search' ? { query: query.trim(), constraints: {}, sort: 'relevance' as const } : {}),
          ...(effectiveKind === 'fetch' ? { target: target.trim() } : {}),
        },
      })
      reset()
      onClose()
    } catch {
      // Kept open so the error stays next to the fields that caused it.
    }
  }

  const ready =
    id.trim() !== '' &&
    displayName.trim() !== '' &&
    channelIds.length > 0 &&
    effectiveKind !== null &&
    (effectiveKind !== 'search' || query.trim() !== '') &&
    (effectiveKind !== 'fetch' || target.trim() !== '')

  return (
    <Modal opened={opened} onClose={onClose} title="新建 View" size="lg" centered>
      <Stack gap="sm">
        <TextInput
          label="View ID"
          description="创建后不可更改，也是 Feed 地址的一部分。"
          placeholder="view_v2ex_daily"
          value={id}
          onChange={(event) => setId(event.currentTarget.value)}
          required
        />
        <TextInput
          label="显示名称"
          placeholder="V2EX 每日跟读"
          value={displayName}
          onChange={(event) => setDisplayName(event.currentTarget.value)}
          required
        />
        <MultiSelect
          label="Channel 范围"
          description="这个 View 只会从选中的线路取数。"
          placeholder={channels.isLoading ? '正在读取…' : '选择 Channel'}
          data={(channels.data ?? []).map((channel) => ({
            value: channel.id,
            label: channel.display_name || channel.id,
          }))}
          value={channelIds}
          onChange={setChannelIds}
          searchable
          required
        />
        <Select
          label="取数方式"
          description={
            effectiveKind
              ? KIND_COPY[effectiveKind].hint
              : '选中的 Channel 没有共同支持的取数方式，请调整范围。'
          }
          data={availableKinds.map((candidate) => ({
            value: candidate,
            label: KIND_COPY[candidate].label,
          }))}
          value={effectiveKind}
          onChange={(value) => value && setKind(value as OperationKind)}
          allowDeselect={false}
          required
        />
        {selected.length > 0 && availableKinds.length < allKinds.length && (
          <Text fz="xs" c="dimmed">
            只列出选中 Channel 的 RouteTemplate 都声明支持的方式。
          </Text>
        )}
        {effectiveKind === 'search' && (
          <TextInput
            label="搜索关键词"
            description="每次刷新都用这个词检索。"
            value={query}
            onChange={(event) => setQuery(event.currentTarget.value)}
            required
          />
        )}
        {effectiveKind === 'fetch' && (
          <TextInput
            label="目标地址"
            description="每次刷新都抓取这个地址。"
            placeholder="https://example.com/article"
            value={target}
            onChange={(event) => setTarget(event.currentTarget.value)}
            required
          />
        )}
        <Group grow align="flex-start">
          <NumberInput
            label="条目上限"
            description="单次执行最多返回多少条，1 到 100。"
            value={limit}
            onChange={(value) => setLimit(typeof value === 'number' ? value : 1)}
            min={1}
            max={100}
          />
          <Select
            label="去重"
            description={DEDUPE_COPY[dedupe].hint}
            data={(['exact', 'none'] as const).map((mode) => ({
              value: mode,
              label: DEDUPE_COPY[mode].label,
            }))}
            value={dedupe}
            onChange={(value) => value && setDedupe(value as 'exact' | 'none')}
            allowDeselect={false}
          />
        </Group>
        <Switch
          label="创建后立即启用"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        {create.error ? <ErrorAlert error={create.error} /> : null}

        <Text fz="xs" c="dimmed">
          以上取数条件保存后不能再改，之后只能改名称和启用状态。要换条件请新建一个 View。
        </Text>

        <Group justify="flex-end" gap="xs" mt="xs">
          <Button variant="default" onClick={onClose}>
            取消
          </Button>
          <Button onClick={() => void submit()} loading={create.isPending} disabled={!ready}>
            创建
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
