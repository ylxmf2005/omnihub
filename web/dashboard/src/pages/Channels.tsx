/** 来源状态只采用服务端就绪度；检查请求本身不能直接把来源标成可用。 */

import { useState } from 'react'
import {
  Anchor,
  Alert,
  Badge,
  Button,
  Group,
  Modal,
  PasswordInput,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconKeyOff, IconPlus, IconRefresh, IconRotate, IconStethoscope, IconTrash } from '@tabler/icons-react'
import { Link } from 'react-router'
import {
  keys,
  READ_ONLY,
  useChannels,
  useBridge,
  useCreate,
  useDelete,
  useEgressProfiles,
  useCredentials,
  useEndpointProfiles,
  useProbeChannel,
  useReadiness,
  useRevokeCredential,
  useRouteTemplates,
  useRun,
  useSources,
  useUpdate,
} from '../api/queries'
import { get } from '../api/client'
import type { Channel, ChannelInput, ChannelHealth, Credential, CredentialInput, RouteTemplate } from '../api/types'
import { ReadinessBadge, RunBadge, Timestamp } from '../components/display'
import {
  ConfirmDialog,
  DemoModeNotice,
  EmptyState,
  ErrorAlert,
  LoadingRow,
  PageHeader,
  SectionCard,
} from '../components/layout'

export function Channels() {
  const channels = useChannels()
  const bridge = useBridge()
  const readiness = useReadiness()
  const templates = useRouteTemplates()
  const [creating, setCreating] = useState(false)

  const healthOf = new Map<string, ChannelHealth>(
    (readiness.data?.channels ?? []).map((entry) => [entry.channel_id, entry]),
  )
  const templateOf = new Map<string, RouteTemplate>(
    (templates.data ?? []).map((template) => [template.route_template_id, template]),
  )
  const list = channels.data ?? []

  return (
    <Stack gap="lg">
      <PageHeader
        title="来源"
        description="连接并管理要搜索或订阅的网站。"
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              onClick={() => {
                void channels.refetch()
                void readiness.refetch()
              }}
              loading={channels.isFetching}
            >
              刷新
            </Button>
            <Button
              leftSection={<IconPlus size={14} />}
              onClick={() => setCreating(true)}
              disabled={READ_ONLY}
            >
              添加来源
            </Button>
          </>
        }
      />

      <DemoModeNotice />
      {channels.error ? (
        <ErrorAlert error={channels.error} onRetry={() => void channels.refetch()} />
      ) : null}

      {bridge.data && (
        <Alert color={bridge.data.connected ? 'teal' : 'yellow'}>
          {bridge.data.connected
            ? 'Chrome 已连接，需要登录的网站可以使用当前浏览器会话。'
            : 'Chrome 尚未连接，需要登录的网站暂时不可用。'}
        </Alert>
      )}

      <SectionCard title="已连接" count={list.length > 0 ? `${list.length} 个` : undefined}>
        {channels.isLoading ? (
          <LoadingRow />
        ) : list.length === 0 ? (
          <EmptyState
            title="还没有来源。"
            hint="添加一个内置网站或公开 Feed。"
            action={
              <Button
                leftSection={<IconPlus size={14} />}
                onClick={() => setCreating(true)}
                disabled={READ_ONLY}
              >
                添加来源
              </Button>
            }
          />
        ) : (
          <Table.ScrollContainer minWidth={720}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>来源</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th>最近检查</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {list.map((channel) => (
                  <ChannelRow
                    key={channel.id}
                    channel={channel}
                    health={healthOf.get(channel.id)}
                    probeable={['feed', 'rsshub'].includes(templateOf.get(channel.route_template_id)?.adapter ?? '')}
                  />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <CredentialsCard />

      <CreateChannelModal opened={creating} onClose={() => setCreating(false)} />
    </Stack>
  )
}

const CREDENTIAL_KINDS = [
  { provider: 'github-api', auth_kind: 'token', label: 'GitHub 访问令牌' },
  { provider: 'tavily', auth_kind: 'api_key', label: 'Tavily API Key' },
  { provider: 'rsshub', auth_kind: 'api_key', label: 'RSSHub 访问密钥' },
  { provider: 'xurl', auth_kind: 'app_only', label: 'X 访问令牌' },
] as const

function CredentialsCard() {
  const credentials = useCredentials()
  const [creating, setCreating] = useState(false)
  return (
    <SectionCard
      title="访问密钥"
      count={credentials.data?.length ? `${credentials.data.length} 个` : undefined}
      action={<Button variant="default" size="compact-sm" leftSection={<IconPlus size={13} />} onClick={() => setCreating(true)} disabled={READ_ONLY}>添加密钥</Button>}
    >
      {credentials.isLoading ? <LoadingRow /> : credentials.error ? <Stack p="md"><ErrorAlert error={credentials.error} onRetry={() => void credentials.refetch()} /></Stack> : !credentials.data?.length ? (
        <EmptyState title="没有保存访问密钥。" hint="公开来源和 Chrome 登录不需要这里配置；只有 API 服务要求密钥时才添加。" />
      ) : (
        <Table.ScrollContainer minWidth={620}>
          <Table verticalSpacing="sm" horizontalSpacing="md">
            <Table.Thead><Table.Tr><Table.Th>名称</Table.Th><Table.Th>状态</Table.Th><Table.Th /></Table.Tr></Table.Thead>
            <Table.Tbody>{credentials.data.map((credential) => <CredentialRow key={credential.id} credential={credential} />)}</Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
      {creating ? <CredentialModal onClose={() => setCreating(false)} /> : null}
    </SectionCard>
  )
}

function CredentialRow({ credential }: { credential: Credential }) {
  const [rotating, setRotating] = useState(false)
  const [revoking, setRevoking] = useState(false)
  return (
    <>
      <Table.Tr>
        <Table.Td><Text fz="sm" fw={500}>{credential.label || CREDENTIAL_KINDS.find((kind) => kind.provider === credential.provider && kind.auth_kind === credential.auth_kind)?.label || '访问密钥'}</Text><Text fz="xs" c="dimmed">{credential.value_masked ?? '未保存值'}</Text></Table.Td>
        <Table.Td><Badge color={credential.enabled && credential.has_value ? 'teal' : 'gray'} variant="light">{credential.enabled && credential.has_value ? '可用' : '已停用'}</Badge></Table.Td>
        <Table.Td><Group gap={4} justify="flex-end" wrap="nowrap"><Button variant="default" size="compact-sm" leftSection={<IconRotate size={13} />} onClick={() => setRotating(true)} disabled={READ_ONLY}>更换</Button>{credential.has_value ? <Button variant="subtle" color="red" size="compact-sm" leftSection={<IconKeyOff size={13} />} onClick={() => setRevoking(true)} disabled={READ_ONLY}>撤销</Button> : null}</Group></Table.Td>
      </Table.Tr>
      {rotating ? <CredentialModal credential={credential} onClose={() => setRotating(false)} /> : null}
      <RevokeCredentialDialog credential={credential} opened={revoking} onClose={() => setRevoking(false)} />
    </>
  )
}

async function credentialEtag(id: string): Promise<string> {
  const current = await get<Credential>(`/v1/credentials/${id}`)
  if (!current.etag) throw new Error('无法确认密钥的当前版本，请刷新后重试')
  return current.etag
}

function CredentialModal({ credential, onClose }: { credential?: Credential; onClose: () => void }) {
  const create = useCreate<CredentialInput, Credential>('/v1/credentials', [keys.credentials])
  const update = useUpdate<CredentialInput, Credential>((id) => `/v1/credentials/${id}`, [keys.credentials])
  const [label, setLabel] = useState(credential?.label ?? '')
  const [kindKey, setKindKey] = useState(credential ? `${credential.provider}/${credential.auth_kind}` : '')
  const [value, setValue] = useState('')
  const [error, setError] = useState<unknown>(null)
  const selected = CREDENTIAL_KINDS.find((kind) => `${kind.provider}/${kind.auth_kind}` === kindKey)

  const submit = async () => {
    if (!selected || !value) return
    setError(null)
    try {
      if (credential) {
        await update.mutateAsync({ id: credential.id, etag: await credentialEtag(credential.id), body: { id: credential.id, provider: credential.provider, auth_kind: credential.auth_kind, value, enabled: true } })
      } else {
        await create.mutateAsync({ id: `credential_${selected.provider.replace(/[^a-z0-9]+/g, '_')}_${Date.now().toString(36)}`, provider: selected.provider, auth_kind: selected.auth_kind, label: label.trim() || undefined, value, enabled: true })
      }
      setValue('')
      onClose()
    } catch (caught) {
      setError(caught)
    }
  }

  return (
    <Modal opened onClose={onClose} title={credential ? '更换访问密钥' : '添加访问密钥'} centered>
      <Stack gap="sm">
        {!credential ? <TextInput label="名称（可选）" value={label} onChange={(event) => setLabel(event.currentTarget.value)} /> : null}
        <Select label="用途" data={CREDENTIAL_KINDS.map((kind) => ({ value: `${kind.provider}/${kind.auth_kind}`, label: kind.label }))} value={kindKey} onChange={(next) => setKindKey(next ?? '')} disabled={Boolean(credential)} required />
        <PasswordInput label="新的密钥" value={value} onChange={(event) => setValue(event.currentTarget.value)} autoComplete="off" required />
        <Text fz="xs" c="dimmed">保存后网页只显示掩码，不能读取或复制原文。需要更换时请重新填写完整值。</Text>
        {error ?? create.error ?? update.error ? <ErrorAlert error={error ?? create.error ?? update.error} /> : null}
        <Group justify="flex-end"><Button variant="default" onClick={onClose}>取消</Button><Button onClick={() => void submit()} loading={create.isPending || update.isPending} disabled={!selected || !value}>保存</Button></Group>
      </Stack>
    </Modal>
  )
}

function RevokeCredentialDialog({ credential, opened, onClose }: { credential: Credential; opened: boolean; onClose: () => void }) {
  const revoke = useRevokeCredential()
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)
  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      await revoke.mutateAsync({ id: credential.id, etag: await credentialEtag(credential.id) })
      onClose()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }
  return <ConfirmDialog opened={opened} onClose={onClose} onConfirm={() => void confirm()} title="撤销访问密钥" target={credential.label || '这个密钥'} consequence="撤销会清空保存的值并停用它。已连接的来源可能因此无法使用。" confirmLabel="撤销" loading={pending} error={error ?? revoke.error} />
}

function ChannelRow({
  channel,
  health,
  probeable,
}: {
  channel: Channel
  health?: ChannelHealth
  probeable: boolean
}) {
  const probe = useProbeChannel()
  const [deleting, setDeleting] = useState(false)

  // Follow the Run created by the probe until it settles, so the row reports the
  // real outcome instead of just "request sent".
  const activeRun = useRun(probe.data?.id, Boolean(probe.data))

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <Anchor component={Link} to={`/sources/${channel.id}`} fz="sm" fw={500}>
            {channel.display_name ?? channel.id}
          </Anchor>
          <Text fz="xs" c="dimmed">{channel.source}</Text>
        </Table.Td>
        <Table.Td>
          <Group gap="xs" wrap="wrap">
            {health ? (
              <ReadinessBadge state={health.readiness} />
            ) : (
              <Text fz="xs" c="dimmed">
                无健康记录
              </Text>
            )}
            {!channel.enabled && (
              <Badge variant="default" size="xs" styles={{ label: { fontWeight: 400 } }}>
                已停用
              </Badge>
            )}
            {activeRun.data && <RunBadge status={activeRun.data.status} />}
          </Group>
        </Table.Td>
        <Table.Td>
          {health?.last_successful_probe_at
            ? <Timestamp iso={health.last_successful_probe_at} relative />
            : <Text fz="xs" c="dimmed">尚未成功检查</Text>}
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="nowrap" justify="flex-end">
            {probeable ? (
              <Button
                variant="default"
                size="compact-sm"
                leftSection={<IconStethoscope size={13} />}
                disabled={READ_ONLY}
                loading={probe.isPending}
                onClick={() => probe.mutate(channel.id)}
              >
                检查
              </Button>
            ) : (
              <Text fz="xs" c="dimmed">
                由实际查询验证
              </Text>
            )}
            <Button
              variant="subtle"
              color="red"
              size="compact-sm"
              px={8}
              disabled={READ_ONLY}
              onClick={() => setDeleting(true)}
              aria-label={`删除 ${channel.display_name ?? channel.id}`}
            >
              <IconTrash size={14} />
            </Button>
          </Group>
        </Table.Td>
      </Table.Tr>

      {probe.error ? (
        <Table.Tr>
          <Table.Td colSpan={4}>
            <ErrorAlert error={probe.error} />
          </Table.Td>
        </Table.Tr>
      ) : null}

      <DeleteChannelDialog
        channel={channel}
        opened={deleting}
        onClose={() => setDeleting(false)}
      />
    </>
  )
}

/**
 * 删除前重新读取强校验值，避免覆盖确认期间发生的并发修改。
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
        throw new Error('无法确认来源的当前版本，请刷新后重试')
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
      title="删除来源"
      target={channel.display_name ?? channel.id}
      consequence="删除后不再从这个来源搜索或更新订阅。仍被订阅使用时不会删除。"
      loading={pending}
      error={etagError ?? remove.error}
    />
  )
}

const ROUTE_LABELS: Record<string, string> = {
  'direct-feed-window': '公开 RSS / Atom',
  'v2ex-direct-latest': 'V2EX 最新主题',
  'nodeseek-direct-latest': 'NodeSeek 最新主题',
  'github-native-search': 'GitHub 官方搜索',
  'tavily-search': 'Tavily Web Search',
  'v2ex-web-search': 'V2EX Web Search',
  'x-xurl-search': 'X 官方搜索',
  'linux-do-discourse-search': 'linux.do 官方搜索',
  'arxiv-native-search': 'arXiv 官方搜索',
  'hn-algolia-search': 'Hacker News Search',
}

function fixedFeedURL(template?: RouteTemplate): string | undefined {
  const properties = template?.parameters_schema?.properties
  if (!properties || typeof properties !== 'object' || Array.isArray(properties)) return undefined
  const urlSchema = (properties as Record<string, unknown>).url
  if (!urlSchema || typeof urlSchema !== 'object' || Array.isArray(urlSchema)) return undefined
  const value = (urlSchema as Record<string, unknown>).const
  return typeof value === 'string' && value !== '' ? value : undefined
}

/**
 * 内置来源复用服务端提供的固定地址，只在确有需要时询问密钥或网络连接。
 */
function CreateChannelModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate<ChannelInput, Channel>('/v1/channels', [keys.channels])
  const egress = useEgressProfiles()
  const credentials = useCredentials()
  const endpoints = useEndpointProfiles()
  const sources = useSources()
  const templates = useRouteTemplates()

  const [displayName, setDisplayName] = useState('')
  const [sourceId, setSourceId] = useState('')
  const [templateId, setTemplateId] = useState('')
  const [url, setUrl] = useState('')
  const [egressId, setEgressId] = useState('')
  const [credentialId, setCredentialId] = useState('')
  const [enabled, setEnabled] = useState(true)

  const manageableTemplates = (templates.data ?? []).filter((template) =>
    ['feed', 'github', 'tavily', 'xurl', 'discourse', 'arxiv', 'hn_algolia'].includes(template.adapter),
  )
  const selectedTemplate = manageableTemplates.find((template) => template.route_template_id === templateId)
  const fixedSource = selectedTemplate?.source_constraint.kind === 'exact'
    ? selectedTemplate.source_constraint.values?.[0]
    : undefined
  const isFeed = selectedTemplate?.adapter === 'feed'
  const presetFeedURL = isFeed ? fixedFeedURL(selectedTemplate) : undefined
  const usesEndpoint = selectedTemplate?.endpoint_required === true
  const matchingEndpoints = (endpoints.data ?? []).filter(
    (endpoint) => endpoint.provider === selectedTemplate?.provider && endpoint.enabled,
  )
  const enabledEgress = (egress.data ?? []).filter((profile) => profile.enabled)
  const needsEndpointEgressChoice = usesEndpoint && (
    matchingEndpoints.length > 1 || matchingEndpoints.length === 0 && enabledEgress.length > 1
  )
  const usesEgress = isFeed || selectedTemplate?.adapter === 'xurl' || needsEndpointEgressChoice
  const matchingCredentials = (credentials.data ?? []).filter(
    (credential) => credential.provider === selectedTemplate?.provider && credential.enabled,
  )

  const reset = () => {
    setDisplayName('')
    setSourceId('')
    setTemplateId('')
    setUrl('')
    setEgressId('')
    setCredentialId('')
    setEnabled(true)
    create.reset()
  }

  const submit = async () => {
    try {
      await create.mutateAsync({
        id: `channel_${sourceId}_${Date.now().toString(36)}`,
        source_id: sourceId.trim(),
        route_template_id: templateId,
        priority: 100,
        enabled,
        display_name: displayName.trim() || undefined,
        url: isFeed ? presetFeedURL ?? (url.trim() || undefined) : undefined,
        egress_profile_id: usesEgress ? egressId || undefined : undefined,
        credential_id: credentialId || undefined,
        parameters: templateId === 'v2ex-web-search' ? { include_domains: ['v2ex.com'] } : undefined,
      })
      reset()
      onClose()
    } catch {
      // Kept open so the error stays next to the fields that caused it.
    }
  }

  const ready = sourceId.trim() !== '' && templateId !== '' &&
    (!isFeed || presetFeedURL !== undefined || url.trim() !== '') && (!usesEgress || egressId !== '') &&
    (selectedTemplate?.auth.required !== true || credentialId !== '')

  return (
    <Modal opened={opened} onClose={onClose} title="添加来源" size="lg" centered>
      <Stack gap="sm">
        <TextInput
          label="名称"
          placeholder="V2EX 最热主题"
          value={displayName}
          onChange={(event) => setDisplayName(event.currentTarget.value)}
        />
        <Select
          label="网站或 Feed"
          placeholder={templates.isLoading ? '正在读取…' : '选择来源'}
          data={manageableTemplates.map((template) => ({
            value: template.route_template_id,
            label: ROUTE_LABELS[template.route_template_id] ?? template.route_template_id,
          }))}
          value={templateId}
          onChange={(value) => {
            const nextTemplateId = value ?? ''
            const nextTemplate = manageableTemplates.find(
              (template) => template.route_template_id === nextTemplateId,
            )
            const nextFixedSource = nextTemplate?.source_constraint.kind === 'exact'
              ? nextTemplate.source_constraint.values?.[0]
              : undefined
            setTemplateId(nextTemplateId)
            setSourceId(nextFixedSource ?? '')
            setUrl('')
            setEgressId('')
            setCredentialId('')
          }}
          required
        />
        {fixedSource ? (
          <Text fz="xs" c="dimmed">
            网站：{fixedSource}
          </Text>
        ) : selectedTemplate ? (
          <Select
            label="网站"
            placeholder={sources.isLoading ? '正在读取…' : '选择网站'}
            data={(sources.data ?? []).map((source) => ({
              value: source.id,
              label: source.display_name || source.id,
            }))}
            value={sourceId}
            onChange={(value) => setSourceId(value ?? '')}
            searchable
            required
          />
        ) : null}
        {isFeed && !presetFeedURL && <TextInput
          label="Feed 地址"
          placeholder="https://www.v2ex.com/index.xml"
          value={url}
          onChange={(event) => setUrl(event.currentTarget.value)}
          required
        />}
        {presetFeedURL && <Text fz="xs" c="dimmed">
          将使用官方 Feed：{presetFeedURL}
        </Text>}
        {selectedTemplate && selectedTemplate.auth.kind !== 'none' && <Select
          label={selectedTemplate.auth.required ? '访问密钥' : '访问密钥（可选）'}
          placeholder={matchingCredentials.length === 0 ? '暂无可用凭据' : '选择凭据'}
          data={matchingCredentials.map((credential) => ({
            value: credential.id,
            label: credential.label || credential.id,
          }))}
          value={credentialId}
          onChange={(value) => setCredentialId(value ?? '')}
          clearable={!selectedTemplate.auth.required}
          required={selectedTemplate.auth.required}
        />}
        {usesEgress && <Select
            label="网络连接"
            description={needsEndpointEgressChoice
              ? '存在多条可用线路，请选择官方服务使用的出口。'
              : 'Feed 和本机命令需要显式选择网络出口。'}
            placeholder={egress.isLoading ? '正在读取…' : '选择网络出口'}
            data={enabledEgress.map((profile) => ({
              value: profile.id,
              label: profile.display_name || profile.id,
            }))}
            value={egressId}
            onChange={(value) => setEgressId(value ?? '')}
            clearable={false}
          />}
        <Switch
          label="添加后立即启用"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        {create.error ? <ErrorAlert error={create.error} /> : null}

        <Group justify="flex-end" gap="xs" mt="xs">
          <Button variant="default" onClick={onClose}>
            取消
          </Button>
          <Button onClick={() => void submit()} loading={create.isPending} disabled={!ready}>
            添加
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
