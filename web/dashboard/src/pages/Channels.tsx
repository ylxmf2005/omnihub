/**
 * Channels — the list, plus creation, probing and deletion.
 *
 * This page is the reference for the write-path conventions the rest of the
 * Dashboard follows:
 *
 *   **The ETag comes from a read, never from a revision number.** A collection
 *   response carries `revision`, and it is tempting to send `"1"` as `If-Match`.
 *   We don't: the handoff requires the strong validator be replayed verbatim, and
 *   a hand-built one silently stops matching the moment the Backend changes how it
 *   derives ETags. So a destructive action first reads the detail route (which
 *   returns the ETag), holds it while the user confirms, and sends that exact
 *   value. The read/confirm gap is precisely the window where a `409
 *   revision_conflict` is meaningful — if someone else edited the Channel while
 *   the dialog was open, the delete must fail loudly rather than proceed.
 *
 *   **A probe is a Run, not a boolean.** Probing returns 202 with a Run; the page
 *   polls it to a terminal status and lets readiness re-derive itself. It never
 *   paints a Channel "ready" on its own — only a real successful Probe does that.
 */

import { useState } from 'react'
import {
  Anchor,
  Badge,
  Box,
  Button,
  Group,
  Modal,
  NumberInput,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPlus, IconRefresh, IconStethoscope, IconTrash } from '@tabler/icons-react'
import { Link } from 'react-router'
import {
  keys,
  READ_ONLY,
  useChannels,
  useCreate,
  useDelete,
  useEgressProfiles,
  useCredentials,
  useEndpointProfiles,
  useProbeChannel,
  useReadiness,
  useRouteTemplates,
  useRun,
  useSources,
} from '../api/queries'
import { get } from '../api/client'
import type { Channel, ChannelInput, ChannelHealth, RouteTemplate } from '../api/types'
import { IdChip, ReadinessBadge, RunBadge, Timestamp } from '../components/display'
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

export function Channels() {
  const channels = useChannels()
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
        title="Channels"
        description="每个 Channel 是一条实际可执行的取数线路：绑定一个 Source、一个 RouteTemplate 和一个出口。"
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
              新建 Channel
            </Button>
          </>
        }
      />

      <DemoModeNotice />
      {channels.error ? (
        <ErrorAlert error={channels.error} onRetry={() => void channels.refetch()} />
      ) : null}

      <SectionCard title="已配置" count={list.length > 0 ? `${list.length} 个` : undefined}>
        {channels.isLoading ? (
          <LoadingRow />
        ) : list.length === 0 ? (
          <EmptyState
            title="还没有 Channel。"
            hint="先创建一条 Direct Feed Channel：给它一个公开的 RSS 或 Atom 地址，再执行一次连通性检查。"
            action={
              <Button
                leftSection={<IconPlus size={14} />}
                onClick={() => setCreating(true)}
                disabled={READ_ONLY}
              >
                新建 Channel
              </Button>
            }
          />
        ) : (
          <Table.ScrollContainer minWidth={720}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>名称</Table.Th>
                  <Table.Th>Source</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th>优先级</Table.Th>
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

      <CreateChannelModal opened={creating} onClose={() => setCreating(false)} />
    </Stack>
  )
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
          <Anchor component={Link} to={`/channels/${channel.id}`} fz="sm" fw={500}>
            {channel.display_name ?? channel.id}
          </Anchor>
          <Box mt={2}>
            <IdChip value={channel.id} width={210} />
          </Box>
        </Table.Td>
        <Table.Td>
          <Text fz="sm">{channel.source}</Text>
          <Text fz="xs" c="dimmed">
            {channel.route_template_id}
          </Text>
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
          {health?.last_successful_probe_at && (
            <MetaLine gap="sm">
              <Fact label="上次成功">
                <Timestamp iso={health.last_successful_probe_at} relative />
              </Fact>
            </MetaLine>
          )}
        </Table.Td>
        <Table.Td>
          <Text fz="sm" ff="monospace">
            {channel.priority}
          </Text>
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
          <Table.Td colSpan={5}>
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
 * Deletion in two steps: read the current detail to capture its ETag, then send
 * that exact validator with the DELETE. The Backend refuses to remove a Channel
 * that a View still references (`resource_in_use`) rather than cascading, and
 * that refusal is surfaced as-is.
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
        throw new Error('Backend 没有返回 ETag，无法安全删除')
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
      title="删除 Channel"
      target={channel.display_name ?? channel.id}
      consequence="删除后这条线路不再参与任何查询。若仍有 View 引用它，Backend 会拒绝删除并告知你，不会连带改动那些 View。"
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
 * 默认流程只收集业务选择。固定官方 Endpoint 由 Backend 在同一事务中复用
 * 或创建；只有真实需要身份或自定义网络出口时才出现对应字段。
 */
function CreateChannelModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate<ChannelInput, Channel>('/v1/channels', [keys.channels])
  const egress = useEgressProfiles()
  const credentials = useCredentials()
  const endpoints = useEndpointProfiles()
  const sources = useSources()
  const templates = useRouteTemplates()

  const [id, setId] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [sourceId, setSourceId] = useState('')
  const [templateId, setTemplateId] = useState('')
  const [url, setUrl] = useState('')
  const [egressId, setEgressId] = useState('')
  const [credentialId, setCredentialId] = useState('')
  const [priority, setPriority] = useState<number>(100)
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
    setId('')
    setDisplayName('')
    setSourceId('')
    setTemplateId('')
    setUrl('')
    setEgressId('')
    setCredentialId('')
    setPriority(100)
    setEnabled(true)
    create.reset()
  }

  const submit = async () => {
    try {
      await create.mutateAsync({
        id: id.trim(),
        source_id: sourceId.trim(),
        route_template_id: templateId,
        priority,
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

  const ready = id.trim() !== '' && sourceId.trim() !== '' && templateId !== '' &&
    (!isFeed || presetFeedURL !== undefined || url.trim() !== '') && (!usesEgress || egressId !== '') &&
    (selectedTemplate?.auth.required !== true || credentialId !== '')

  return (
    <Modal opened={opened} onClose={onClose} title="新建 Channel" size="lg" centered>
      <Stack gap="sm">
        <TextInput
          label="Channel ID"
          description="创建后不可更改，用于在 Operation 和 View 中引用这条线路。"
          placeholder="channel_v2ex_hot"
          value={id}
          onChange={(event) => setId(event.currentTarget.value)}
          required
        />
        <TextInput
          label="显示名称"
          placeholder="V2EX 最热主题"
          value={displayName}
          onChange={(event) => setDisplayName(event.currentTarget.value)}
        />
        <Select
          label="接入方式"
          description="官方服务地址会自动复用或创建，不需要另外配置 Endpoint。"
          placeholder={templates.isLoading ? '正在读取…' : '选择一种接入方式'}
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
            内容来源：{fixedSource}
          </Text>
        ) : selectedTemplate ? (
          <Select
            label="内容来源"
            description="选择这条线路归属的站点。"
            placeholder={sources.isLoading ? '正在读取…' : '选择来源'}
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
          description="公开可访问的 RSS 或 Atom 地址。"
          placeholder="https://www.v2ex.com/index.xml"
          value={url}
          onChange={(event) => setUrl(event.currentTarget.value)}
          required
        />}
        {presetFeedURL && <Text fz="xs" c="dimmed">
          官方 Feed：{presetFeedURL}
        </Text>}
        {selectedTemplate && selectedTemplate.auth.kind !== 'none' && <Select
          label={selectedTemplate.auth.required ? '访问凭据' : '访问凭据（可选）'}
          description={selectedTemplate.auth.required ? '这个来源要求凭据。' : '匿名访问受限时再选择凭据。'}
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
        <Group grow align="flex-start">
          {usesEgress && <Select
            label="网络出口"
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
          <NumberInput
            label="优先级"
            description="同一 Source 下数值大的先被选用。"
            value={priority}
            onChange={(value) => setPriority(typeof value === 'number' ? value : 0)}
            min={0}
            max={1000}
          />
        </Group>
        <Switch
          label="创建后立即启用"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        {create.error ? <ErrorAlert error={create.error} /> : null}

        <Text fz="xs" c="dimmed">
          创建后可直接在 Query Workbench 运行一次真实查询。Feed 与 RSSHub 还支持单独的连通性检查。
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
