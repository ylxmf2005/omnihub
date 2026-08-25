import { useEffect, useState } from 'react'
import { Alert, Anchor, Badge, Button, Group, Modal, Stack, Switch, Text, TextInput } from '@mantine/core'
import { IconArrowLeft, IconPencil, IconRefresh, IconStethoscope, IconTrash } from '@tabler/icons-react'
import { Link, useNavigate, useParams } from 'react-router'
import { get } from '../api/client'
import {
  keys,
  READ_ONLY,
  useChannel,
  useChannels,
  useChromeDescriptor,
  useDelete,
  useProbeChannel,
  useReadiness,
  useRouteTemplates,
  useRun,
  useUpdate,
} from '../api/queries'
import type { Channel, ChannelInput } from '../api/types'
import { ReadinessBadge, RunBadge, Timestamp } from '../components/display'
import { CardRow, ConfirmDialog, EmptyState, ErrorAlert, LoadingRow, PageHeader, SectionCard } from '../components/layout'

export function ChannelDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const detail = useChannel(id)
  const channels = useChannels()
  const readiness = useReadiness()
  const templates = useRouteTemplates()
  const probe = useProbeChannel()
  const activeRun = useRun(probe.data?.id, Boolean(probe.data))
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const channel = detail.data?.data ?? (channels.data ?? []).find((entry) => entry.id === id)
  const health = (readiness.data?.channels ?? []).find((entry) => entry.channel_id === id)
  const template = (templates.data ?? []).find((entry) => entry.route_template_id === channel?.route_template_id)
  const probeable = template ? ['feed', 'rsshub'].includes(template.adapter) : false
  const descriptor = useChromeDescriptor(template?.auth.kind === 'browser_cookie' ? channel?.id : undefined)

  if (detail.isLoading && !channel) {
    return <Stack gap="lg"><PageHeader title="来源" /><LoadingRow /></Stack>
  }

  if (!channel) {
    return (
      <Stack gap="lg">
        <PageHeader title="来源" />
        {detail.error ? <ErrorAlert error={detail.error} onRetry={() => void detail.refetch()} /> : (
          <SectionCard title="找不到这个来源">
            <EmptyState title="它可能已被删除。" action={<Anchor component={Link} to="/sources">返回来源</Anchor>} />
          </SectionCard>
        )}
      </Stack>
    )
  }

  return (
    <Stack gap="lg">
      <PageHeader
        title={channel.display_name ?? channel.source}
        description={channel.source}
        actions={
          <>
            <Button variant="default" component={Link} to="/sources" leftSection={<IconArrowLeft size={14} />}>返回</Button>
            <Button variant="default" leftSection={<IconRefresh size={14} />} loading={detail.isFetching || readiness.isFetching} onClick={() => {
              void detail.refetch()
              void readiness.refetch()
            }}>刷新</Button>
            <Button variant="default" leftSection={<IconPencil size={14} />} disabled={READ_ONLY} onClick={() => setEditing(true)}>编辑</Button>
            <Button variant="subtle" color="red" leftSection={<IconTrash size={14} />} disabled={READ_ONLY} onClick={() => setDeleting(true)}>删除</Button>
          </>
        }
      />

      {detail.error ? <ErrorAlert error={detail.error} onRetry={() => void detail.refetch()} /> : null}
      {probe.error ? <ErrorAlert error={probe.error} /> : null}

      <SectionCard title="连接状态">
        <CardRow>
          <Stack gap="sm">
            <Group gap="xs">
              {health ? <ReadinessBadge state={health.readiness} /> : <Badge variant="default">尚未检查</Badge>}
              {!channel.enabled && <Badge variant="default">已停用</Badge>}
              {activeRun.data && <RunBadge status={activeRun.data.status} />}
            </Group>
            {health?.last_successful_probe_at ? (
              <Text fz="sm" c="dimmed">上次成功检查：<Timestamp iso={health.last_successful_probe_at} relative /></Text>
            ) : <Text fz="sm" c="dimmed">还没有成功检查记录。</Text>}
            {health?.action_required?.url ? <Anchor href={health.action_required.url} target="_blank" rel="noreferrer" fz="sm">完成登录或授权</Anchor> : null}
            {probeable ? (
              <Group><Button leftSection={<IconStethoscope size={14} />} loading={probe.isPending} disabled={READ_ONLY} onClick={() => probe.mutate(channel.id)}>检查连接</Button></Group>
            ) : <Text fz="xs" c="dimmed">这个来源会在实际搜索或刷新订阅时验证连接。</Text>}
          </Stack>
        </CardRow>
        {health?.checks.length ? (
          <CardRow>
            <details>
              <summary>查看检查详情</summary>
              <Stack gap="xs" mt="sm">
                {health.checks.map((check, index) => (
                  <Text key={`${check.kind}-${index}`} fz="sm" c={check.status === 'failed' ? 'red' : 'dimmed'}>
                    {check.status === 'passed' ? '已通过' : check.status === 'failed' ? '未通过' : '未验证'}
                    {check.error?.message ? `：${check.error.message}` : ''}
                  </Text>
                ))}
              </Stack>
            </details>
          </CardRow>
        ) : null}
      </SectionCard>

      {descriptor.data ? (
        <Alert color="blue" title="使用 Chrome 登录状态">
          OmniHub 不保存 Cookie。请先在 Chrome 中{' '}
          <Anchor href={descriptor.data.data.login_url} target="_blank" rel="noreferrer">登录这个网站</Anchor>
          ，再由扩展授权读取当前会话。
        </Alert>
      ) : null}

      <EditSourceModal channel={channel} etag={detail.data?.etag ?? null} opened={editing} onClose={() => setEditing(false)} />
      <DeleteSourceDialog channel={channel} opened={deleting} onClose={() => setDeleting(false)} onDeleted={() => void navigate('/sources')} />
    </Stack>
  )
}

function EditSourceModal({ channel, etag, opened, onClose }: { channel: Channel; etag: string | null; opened: boolean; onClose: () => void }) {
  const update = useUpdate<ChannelInput, Channel>((id) => `/v1/channels/${id}`, [keys.channels, keys.channel(channel.id)])
  const [name, setName] = useState(channel.display_name ?? '')
  const [enabled, setEnabled] = useState(channel.enabled)
  const parameters = channel.parameters ?? {}
  const unsupportedParameters = Object.keys(parameters).filter((key) => key !== 'url')
  const url = typeof parameters.url === 'string' ? parameters.url : undefined

  useEffect(() => {
    if (!opened) return
    setName(channel.display_name ?? '')
    setEnabled(channel.enabled)
    update.reset()
  }, [opened, channel])

  const submit = async () => {
    if (!etag) return
    try {
      await update.mutateAsync({
        id: channel.id,
        etag,
        body: {
          id: channel.id,
          source_id: channel.source,
          route_template_id: channel.route_template_id,
          priority: channel.priority,
          enabled,
          display_name: name.trim() || undefined,
          url,
          egress_profile_id: channel.egress_profile_id,
          endpoint_profile_id: channel.endpoint_profile_id,
          credential_id: channel.credential_id,
        },
      })
      onClose()
    } catch {
      // 错误留在表单内，用户可以保留输入后重试。
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title="编辑来源" centered>
      <Stack gap="sm">
        <TextInput label="名称" value={name} onChange={(event) => setName(event.currentTarget.value)} />
        <Switch label="启用" checked={enabled} onChange={(event) => setEnabled(event.currentTarget.checked)} />
        {unsupportedParameters.length > 0 ? <Alert color="yellow">这个来源包含只能由命令行修改的配置，网页不会覆盖它。</Alert> : null}
        {!etag ? <Alert color="yellow">请刷新页面后再保存。</Alert> : null}
        {update.error ? <ErrorAlert error={update.error} /> : null}
        <Group justify="flex-end"><Button variant="default" onClick={onClose}>取消</Button><Button onClick={() => void submit()} loading={update.isPending} disabled={!etag || unsupportedParameters.length > 0}>保存</Button></Group>
      </Stack>
    </Modal>
  )
}

function DeleteSourceDialog({ channel, opened, onClose, onDeleted }: { channel: Channel; opened: boolean; onClose: () => void; onDeleted: () => void }) {
  const remove = useDelete((id) => `/v1/channels/${id}`, [keys.channels])
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const current = await get<Channel>(`/v1/channels/${channel.id}`)
      if (!current.etag) throw new Error('无法确认来源的当前版本，请刷新后重试')
      await remove.mutateAsync({ id: channel.id, etag: current.etag })
      onClose()
      onDeleted()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }

  return <ConfirmDialog opened={opened} onClose={onClose} onConfirm={() => void confirm()} title="删除来源" target={channel.display_name ?? channel.source} consequence="删除后不再从这里搜索或更新订阅。仍被订阅使用时不会删除。" loading={pending} error={error ?? remove.error} />
}
