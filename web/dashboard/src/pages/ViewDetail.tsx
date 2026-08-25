import { useEffect, useState } from 'react'
import {
  Alert,
  Anchor,
  Badge,
  Button,
  CopyButton,
  Group,
  Modal,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconArrowLeft, IconCheck, IconCopy, IconPencil, IconRefresh, IconTrash } from '@tabler/icons-react'
import { Link, useNavigate, useParams } from 'react-router'
import { get } from '../api/client'
import {
  keys,
  READ_ONLY,
  useDelete,
  useRefreshView,
  useRun,
  useUpdate,
  useView,
  useViewItems,
} from '../api/queries'
import type { Item, View, ViewInput } from '../api/types'
import { RunBadge, Timestamp, ViewBadge } from '../components/display'
import { CardRow, ConfirmDialog, EmptyState, ErrorAlert, LoadingRow, PageHeader, SectionCard } from '../components/layout'
import { KIND_COPY } from './Views'

export function ViewDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const detail = useView(id)
  const items = useViewItems(id)
  const refresh = useRefreshView()
  const startedRun = useRun(refresh.data?.id, Boolean(refresh.data))
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const settledRun = startedRun.data && ['complete', 'partial', 'failed', 'cancelled'].includes(startedRun.data.status)
    ? startedRun.data.id
    : null
  useEffect(() => {
    if (!settledRun) return
    void detail.refetch()
    void items.refetch()
  }, [settledRun])

  if (detail.isLoading) return <Stack gap="lg"><PageHeader title="订阅" /><LoadingRow /></Stack>
  if (!detail.data) {
    return (
      <Stack gap="lg">
        <PageHeader title="订阅" actions={<Button variant="default" component={Link} to="/subscriptions">返回</Button>} />
        {detail.error ? <ErrorAlert error={detail.error} onRetry={() => void detail.refetch()} /> : <EmptyState title="找不到这个订阅。" />}
      </Stack>
    )
  }

  const { view, status, feed_urls: feeds, snapshot } = detail.data.data
  const running = startedRun.data ?? detail.data.data.active_run ?? null

  return (
    <Stack gap="lg">
      <PageHeader
        title={view.display_name}
        description={view.operation.query ? `关键词：${view.operation.query}` : KIND_COPY[view.operation.operation].hint}
        actions={
          <>
            <Button variant="default" component={Link} to="/subscriptions" leftSection={<IconArrowLeft size={14} />}>返回</Button>
            <Button variant="default" leftSection={<IconPencil size={14} />} disabled={READ_ONLY} onClick={() => setEditing(true)}>编辑</Button>
            <Button variant="subtle" color="red" leftSection={<IconTrash size={14} />} disabled={READ_ONLY} onClick={() => setDeleting(true)}>删除</Button>
            <Button leftSection={<IconRefresh size={14} />} disabled={READ_ONLY || !view.enabled} loading={refresh.isPending} onClick={() => refresh.mutate(view.id)}>立即更新</Button>
          </>
        }
      />

      {refresh.error ? <ErrorAlert error={refresh.error} /> : null}
      <SectionCard title="状态">
        <CardRow>
          <Stack gap="sm">
            <Group gap="xs"><ViewBadge status={status} />{running && <RunBadge status={running.status} />}{!view.enabled && <Badge variant="default">已停用</Badge>}</Group>
            {snapshot ? <Text fz="sm" c="dimmed">上次更新：<Timestamp iso={snapshot.created_at} relative /></Text> : <Text fz="sm" c="dimmed">还没有成功更新过。</Text>}
            {running ? <Anchor component={Link} to={`/activity/${running.id}`} fz="sm">查看这次活动</Anchor> : null}
          </Stack>
        </CardRow>
      </SectionCard>

      <SectionCard title="Feed 地址">
        {([
          ['RSS', feeds.rss],
          ['Atom', feeds.atom],
          ['JSON Feed', feeds.json],
        ] as const).map(([label, value]) => (
          <CardRow key={label}>
            <Group justify="space-between" wrap="nowrap">
              <Stack gap={2} style={{ minWidth: 0 }}><Text fz="xs" c="dimmed">{label}</Text><Text fz="sm" truncate>{value}</Text></Stack>
              <CopyButton value={value}>{({ copied, copy }) => <Button variant="default" size="compact-sm" leftSection={copied ? <IconCheck size={13} /> : <IconCopy size={13} />} onClick={copy}>{copied ? '已复制' : '复制'}</Button>}</CopyButton>
            </Group>
          </CardRow>
        ))}
      </SectionCard>

      <SectionCard title="当前内容" count={items.data ? `${items.data.items.length} 条` : undefined}>
        {items.isLoading ? <LoadingRow /> : items.error ? <CardRow><ErrorAlert error={items.error} onRetry={() => void items.refetch()} /></CardRow> : !items.data?.items.length ? (
          <EmptyState title="还没有内容。" hint="立即更新一次后，结果会出现在这里。" />
        ) : (
          <Table.ScrollContainer minWidth={620}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead><Table.Tr><Table.Th>标题</Table.Th><Table.Th>来源</Table.Th><Table.Th>发布时间</Table.Th></Table.Tr></Table.Thead>
              <Table.Tbody>{items.data.items.map((item) => <ItemRow key={item.id} item={item} />)}</Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      {items.data?.stale ? <Alert color="yellow">当前内容已超过新鲜期，可以继续阅读；更新订阅即可获取最新结果。</Alert> : null}
      <EditSubscriptionModal view={view} etag={detail.data.etag} opened={editing} onClose={() => setEditing(false)} />
      <DeleteSubscriptionDialog view={view} opened={deleting} onClose={() => setDeleting(false)} onDeleted={() => void navigate('/subscriptions')} />
    </Stack>
  )
}

function ItemRow({ item }: { item: Item }) {
  const source = item.observations[0]?.source ?? '未知来源'
  return (
    <Table.Tr>
      <Table.Td><Anchor href={item.url} target="_blank" rel="noreferrer" fz="sm" fw={500}>{item.title || item.url}</Anchor>{item.content?.text ? <Text fz="xs" c="dimmed" lineClamp={2} mt={3}>{item.content.text}</Text> : null}</Table.Td>
      <Table.Td><Text fz="sm">{source}</Text></Table.Td>
      <Table.Td>{item.published_at ? <Timestamp iso={item.published_at} /> : <Text fz="xs" c="dimmed">未提供</Text>}</Table.Td>
    </Table.Tr>
  )
}

function EditSubscriptionModal({ view, etag, opened, onClose }: { view: View; etag: string | null; opened: boolean; onClose: () => void }) {
  const update = useUpdate<ViewInput, View>((id) => `/v1/views/${id}`, [keys.views, keys.view(view.id)])
  const [name, setName] = useState(view.display_name)
  const [enabled, setEnabled] = useState(view.enabled)
  useEffect(() => {
    if (!opened) return
    setName(view.display_name)
    setEnabled(view.enabled)
    update.reset()
  }, [opened, view])

  const submit = async () => {
    if (!etag) return
    try {
      await update.mutateAsync({ id: view.id, etag, body: { id: view.id, display_name: name.trim(), operation: view.operation, enabled } })
      onClose()
    } catch {
      // 错误留在表单内，用户可以保留输入后重试。
    }
  }
  return <Modal opened={opened} onClose={onClose} title="编辑订阅" centered><Stack gap="sm"><TextInput label="名称" value={name} onChange={(event) => setName(event.currentTarget.value)} required /><Switch label="启用" checked={enabled} onChange={(event) => setEnabled(event.currentTarget.checked)} />{update.error ? <ErrorAlert error={update.error} /> : null}<Group justify="flex-end"><Button variant="default" onClick={onClose}>取消</Button><Button onClick={() => void submit()} loading={update.isPending} disabled={!etag || !name.trim()}>保存</Button></Group></Stack></Modal>
}

function DeleteSubscriptionDialog({ view, opened, onClose, onDeleted }: { view: View; opened: boolean; onClose: () => void; onDeleted: () => void }) {
  const remove = useDelete((id) => `/v1/views/${id}`, [keys.views])
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)
  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const current = await get<unknown>(`/v1/views/${view.id}`)
      if (!current.etag) throw new Error('无法确认订阅的当前版本，请刷新后重试')
      await remove.mutateAsync({ id: view.id, etag: current.etag })
      onClose()
      onDeleted()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }
  return <ConfirmDialog opened={opened} onClose={onClose} onConfirm={() => void confirm()} title="删除订阅" target={view.display_name} consequence="删除后 Feed 地址立即失效。已有活动记录时不会删除。" loading={pending} error={error ?? remove.error} />
}
