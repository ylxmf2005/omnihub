/**
 * Connections — Egress profiles and Endpoint profiles.
 *
 * They share a page because they answer one question together: how does OmniHub
 * reach the outside. An Egress is the route out (direct, environment, or a fixed
 * proxy); an Endpoint is a specific service address that is pinned to one Egress.
 *
 * Three contract facts, verified against the live Backend, drive the forms:
 *
 *   **A proxy address is write-only.** `GET /v1/egress-profiles` returns
 *   `has_endpoint` and `proxied` but never the address itself, by design. Since
 *   PUT is a full replacement, editing a proxy profile means re-entering the
 *   address — the form says so rather than silently blanking it.
 *
 *   **`POST /v1/endpoint-profiles` is three different operations behind one
 *   path.** `rsshub` takes any http(s) base URL and requires a `trust` value;
 *   `embedding` takes a user-supplied URL and has `trust` forced to `user`; the
 *   official providers accept only their own fixed origin and reject any `trust`
 *   override. A single free-form form here would produce refusals in two of the
 *   three cases, so the form branches on the provider the user picks.
 *
 *   **`enabled` is not a writable field on an Endpoint.** The Backend sets it to
 *   true on every apply, so there is no switch for it here.
 */

import { useState } from 'react'
import {
  Button,
  Group,
  Modal,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPlus, IconRefresh, IconTrash } from '@tabler/icons-react'
import {
  keys,
  READ_ONLY,
  useCreate,
  useCredentials,
  useDelete,
  useEgressProfiles,
  useEndpointProfiles,
  useUpdate,
} from '../api/queries'
import { get } from '../api/client'
import type {
  Credential,
  EgressProfile,
  EgressProfileInput,
  EndpointProfile,
  EndpointProfileInput,
} from '../api/types'
import { IdChip, StateBadge } from '../components/display'
import {
  Caveat,
  ConfirmDialog,
  DemoModeNotice,
  EmptyState,
  ErrorAlert,
  Fact,
  LoadingRow,
  MetaLine,
  PageHeader,
  PageLink,
  SectionCard,
} from '../components/layout'

const EGRESS_MODES: { value: EgressProfile['mode']; label: string; meaning: string }[] = [
  { value: 'direct', label: '直连', meaning: '不经任何代理，直接连接目标。' },
  {
    value: 'environment',
    label: '跟随环境变量',
    meaning: '由进程的代理环境变量决定。OmniHub 不猜测某次请求是否真的走了代理。',
  },
  { value: 'http_proxy', label: 'HTTP 代理', meaning: '固定经这个 HTTP 代理出站。' },
  { value: 'socks5', label: 'SOCKS5 代理', meaning: '固定经这个 SOCKS5 代理出站。' },
]

const SOCKS5_DNS_MODES = [
  { value: 'local', label: '本机解析域名' },
  { value: 'proxy', label: '由代理解析域名' },
]

function egressModeCopy(mode: string): { label: string; meaning: string } {
  return EGRESS_MODES.find((entry) => entry.value === mode) ?? { label: mode, meaning: '' }
}

function isProxyMode(mode: string): boolean {
  return mode === 'http_proxy' || mode === 'socks5'
}

/**
 * The Endpoint families `POST /v1/endpoint-profiles` supports. `officialOrigin`
 * is the only base URL the Backend accepts for that provider — it is fixed in
 * the Backend, so the form prefills it and does not let it be edited. `xurl` is
 * absent because it runs as a local executable and has no endpoint origin to
 * configure.
 */
const ENDPOINT_KINDS: {
  provider: string
  label: string
  description: string
  officialOrigin?: string
  urlHint?: string
}[] = [
  {
    provider: 'rsshub',
    label: 'RSSHub 实例',
    description: '你自己部署或选定的 RSSHub 地址。',
    urlHint: 'http(s) 地址，不能带查询串或片段。',
  },
  {
    provider: 'embedding',
    label: 'embedding Endpoint',
    description: '为 Semantic Profile 计算向量的服务。',
    urlHint: '必须是 HTTPS。只有指向字面 loopback IP 时才允许 HTTP，且出口必须是直连。',
  },
  {
    provider: 'github-api',
    label: 'GitHub API',
    description: '官方地址固定，不可更改。',
    officialOrigin: 'https://api.github.com',
  },
  {
    provider: 'tavily',
    label: 'Tavily',
    description: '官方地址固定，不可更改。',
    officialOrigin: 'https://api.tavily.com',
  },
	{
		provider: 'discourse',
		label: 'linux.do Discourse Search',
		description: 'linux.do 官方站内搜索接口；可能受 Cloudflare 挑战影响。',
		officialOrigin: 'https://linux.do',
	},
	{
		provider: 'arxiv-api',
		label: 'arXiv Query API',
		description: 'arXiv 官方检索接口。',
		officialOrigin: 'https://export.arxiv.org',
	},
	{
		provider: 'hn-algolia',
		label: 'Hacker News Algolia',
		description: 'Hacker News 官网采用的 Algolia 派生索引。',
		officialOrigin: 'https://hn.algolia.com',
	},
]

/**
 * `trust` for an RSSHub Endpoint is a required free-form string; these are the
 * two values the shipped route catalog actually uses for a feed-style endpoint.
 */
const RSSHUB_TRUST = [
  { value: 'configured_endpoint', label: '你自己配置的实例' },
  { value: 'remote_public', label: '公共实例' },
]

const TRUST_COPY: Record<string, string> = {
  official: 'Provider 官方地址',
  user: '你自己配置的地址',
  configured_endpoint: '你自己配置的实例',
  remote_public: '公共实例',
  official_api: 'Provider 官方 API',
  local_executable: '本机可执行程序',
}

/** Reads a resource purely to capture its strong ETag for a following write. */
async function readEtag(path: string): Promise<string> {
  const current = await get<unknown>(path)
  if (!current.etag) {
    throw new Error('Backend 没有返回 ETag，无法安全写入')
  }
  return current.etag
}

export function Connections() {
  const egress = useEgressProfiles()
  const endpoints = useEndpointProfiles()
  const [creatingEgress, setCreatingEgress] = useState(false)
  const [creatingEndpoint, setCreatingEndpoint] = useState(false)

  const egressList = egress.data ?? []
  const endpointList = endpoints.data ?? []

  const egressNameOf = (id?: string) => {
    if (!id) return undefined
    const found = egressList.find((profile) => profile.id === id)
    return found?.display_name || id
  }

  return (
    <Stack gap="lg">
      <PageHeader
        title="Connections"
        description="Egress 决定从哪条线路出站，Endpoint 是一个绑定了固定出口的具体服务地址。"
        actions={
          <Button
            variant="default"
            leftSection={<IconRefresh size={14} />}
            onClick={() => {
              void egress.refetch()
              void endpoints.refetch()
            }}
            loading={egress.isFetching || endpoints.isFetching}
          >
            刷新
          </Button>
        }
      />

      <DemoModeNotice />
      {egress.error ? (
        <ErrorAlert error={egress.error} onRetry={() => void egress.refetch()} />
      ) : null}

      <SectionCard
        title="Egress"
        count={egressList.length > 0 ? `${egressList.length} 条` : undefined}
        action={
          <Button
            size="compact-sm"
            leftSection={<IconPlus size={13} />}
            onClick={() => setCreatingEgress(true)}
            disabled={READ_ONLY}
          >
            新建 Egress
          </Button>
        }
      >
        {egress.isLoading ? (
          <LoadingRow />
        ) : egressList.length === 0 ? (
          <EmptyState
            title="还没有 Egress。"
            hint="Egress 是一条出站线路：直连、跟随系统代理环境变量，或固定走一个 HTTP / SOCKS5 代理。"
          />
        ) : (
          <Table.ScrollContainer minWidth={780}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>名称</Table.Th>
                  <Table.Th>出站方式</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {egressList.map((profile) => (
                  <EgressRow key={profile.id} profile={profile} />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <Caveat>
        出站失败时 Backend 不会静默降级：不会改走直连，不会切到公共 DoH 解析，也不会关闭 TLS 校验。
        它会如实报错，让你决定怎么处理。
      </Caveat>

      {endpoints.error ? (
        <ErrorAlert error={endpoints.error} onRetry={() => void endpoints.refetch()} />
      ) : null}

      <SectionCard
        title="Endpoint"
        count={endpointList.length > 0 ? `${endpointList.length} 个` : undefined}
        action={
          <Button
            size="compact-sm"
            leftSection={<IconPlus size={13} />}
            onClick={() => setCreatingEndpoint(true)}
            disabled={READ_ONLY || egressList.length === 0}
          >
            新建 Endpoint
          </Button>
        }
      >
        {endpoints.isLoading ? (
          <LoadingRow />
        ) : endpointList.length === 0 ? (
          <EmptyState
            title="还没有 Endpoint。"
            hint="Endpoint 把一个具体服务地址固定到一条出口上：自建的 RSSHub 实例、为 Semantic Profile 计算向量的 embedding 服务，或需要指定出口的官方 API。"
          />
        ) : (
          <Table.ScrollContainer minWidth={860}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>名称</Table.Th>
                  <Table.Th>地址</Table.Th>
                  <Table.Th>Egress</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {endpointList.map((row) => (
                  <EndpointTableRow
                    key={row.id}
                    row={row}
                    egressName={egressNameOf(row.egress_profile_id)}
                  />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <Caveat>
        绑定了 Endpoint 的 Channel 会继承这个 Endpoint 固定的 Egress，单次 Operation 无法覆盖它。
        要换出口，改这里的 Endpoint。
      </Caveat>

      {creatingEgress && <EgressModal onClose={() => setCreatingEgress(false)} />}
      {creatingEndpoint && (
        <CreateEndpointModal
          egressList={egressList}
          onClose={() => setCreatingEndpoint(false)}
        />
      )}
    </Stack>
  )
}

function EgressRow({ profile }: { profile: EgressProfile }) {
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const mode = egressModeCopy(profile.mode)

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <Text fz="sm" fw={500}>
            {profile.display_name || profile.id}
          </Text>
          <IdChip value={profile.id} width={200} />
        </Table.Td>
        <Table.Td>
          <Text fz="sm">{mode.label}</Text>
          <MetaLine gap="sm">
            {profile.has_endpoint && <Fact label="代理地址">已配置</Fact>}
            {profile.proxied && <Fact label="固定代理">是</Fact>}
          </MetaLine>
          <Text fz={10} c="dimmed" ff="monospace">
            {profile.mode}
          </Text>
        </Table.Td>
        <Table.Td>
          {profile.enabled ? (
            <StateBadge
              label="启用"
              tone="ok"
              meaning="可以被 Channel 与 Endpoint 引用。"
              code="enabled=true"
            />
          ) : (
            <StateBadge
              label="已停用"
              tone="idle"
              meaning="配置仍在，但不会被选用。"
              code="enabled=false"
            />
          )}
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="nowrap" justify="flex-end">
            <Button
              variant="default"
              size="compact-sm"
              disabled={READ_ONLY}
              onClick={() => setEditing(true)}
            >
              编辑
            </Button>
            <Button
              variant="subtle"
              color="red"
              size="compact-sm"
              px={8}
              disabled={READ_ONLY}
              onClick={() => setDeleting(true)}
              aria-label={`删除 ${profile.display_name || profile.id}`}
            >
              <IconTrash size={14} />
            </Button>
          </Group>
        </Table.Td>
      </Table.Tr>

      {editing && <EgressModal existing={profile} onClose={() => setEditing(false)} />}
      <DeleteEgressDialog
        profile={profile}
        opened={deleting}
        onClose={() => setDeleting(false)}
      />
    </>
  )
}

function EndpointTableRow({ row, egressName }: { row: EndpointProfile; egressName?: string }) {
  const [deleting, setDeleting] = useState(false)

  const kind = ENDPOINT_KINDS.find((entry) => entry.provider === row.provider)

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <Text fz="sm" fw={500}>
            {kind?.label ?? row.provider}
          </Text>
          <IdChip value={row.id} width={200} />
        </Table.Td>
        <Table.Td>
          <Text fz="sm" style={{ wordBreak: 'break-all' }}>
            {row.base_url ?? '未设置'}
          </Text>
          {row.trust && (
            <MetaLine gap="sm">
              <Fact label="来源">{TRUST_COPY[row.trust] ?? row.trust}</Fact>
            </MetaLine>
          )}
        </Table.Td>
        <Table.Td>
          {egressName ? (
            <Text fz="sm">{egressName}</Text>
          ) : (
            <Text fz="xs" c="dimmed">
              未绑定
            </Text>
          )}
        </Table.Td>
        <Table.Td>
          {row.enabled ? (
            <StateBadge
              label="启用"
              tone="ok"
              meaning="可以被 Channel 与 Semantic Profile 引用。"
              code="enabled=true"
            />
          ) : (
            <StateBadge
              label="已停用"
              tone="idle"
              meaning="配置仍在，但不会被选用。"
              code="enabled=false"
            />
          )}
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="nowrap" justify="flex-end">
            <Button
              variant="subtle"
              color="red"
              size="compact-sm"
              px={8}
              disabled={READ_ONLY}
              onClick={() => setDeleting(true)}
              aria-label={`删除 ${row.id}`}
            >
              <IconTrash size={14} />
            </Button>
          </Group>
        </Table.Td>
      </Table.Tr>

      <DeleteEndpointDialog row={row} opened={deleting} onClose={() => setDeleting(false)} />
    </>
  )
}

/**
 * Create and edit share one modal because the Backend shares one shape: PUT is a
 * full replacement, so an edit has to send every field a create sends. The
 * proxy address is the one thing that cannot be prefilled — the Backend never
 * echoes it back — so on edit the form asks for it again and says why.
 */
function EgressModal({
  existing,
  onClose,
}: {
  existing?: EgressProfile
  onClose: () => void
}) {
  const create = useCreate<EgressProfileInput, unknown>('/v1/egress-profiles', [keys.egress])
  const update = useUpdate<EgressProfileInput, unknown>(
    (id) => `/v1/egress-profiles/${id}`,
    [keys.egress],
  )
  const credentials = useCredentials()

  const [id, setId] = useState(existing?.id ?? '')
  const [displayName, setDisplayName] = useState(existing?.display_name ?? '')
  const [mode, setMode] = useState<string>(existing?.mode ?? 'direct')
  const [proxyEndpoint, setProxyEndpoint] = useState('')
  const [socks5Dns, setSocks5Dns] = useState('local')
  const [credentialId, setCredentialId] = useState('')
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  // Only `egress` / `basic` credentials can authenticate a proxy; offering any
  // other kind here would produce a refusal the user cannot interpret.
  const proxyCredentials = (credentials.data ?? []).filter(
    (entry: Credential) => entry.provider === 'egress' && entry.auth_kind === 'basic',
  )

  const proxied = isProxyMode(mode)

  const body = (): EgressProfileInput => ({
    id: id.trim(),
    mode,
    enabled,
    display_name: displayName.trim() || undefined,
    // direct and environment must carry no proxy configuration at all: the
    // Backend rejects the write rather than ignoring the extra fields.
    proxy_endpoint: proxied ? proxyEndpoint.trim() : undefined,
    socks5_dns: mode === 'socks5' ? socks5Dns : undefined,
    credential_id: proxied && credentialId ? credentialId : undefined,
  })

  const submit = async () => {
    setPending(true)
    setError(null)
    try {
      if (existing) {
        const etag = await readEtag(`/v1/egress-profiles/${existing.id}`)
        await update.mutateAsync({ id: existing.id, etag, body: body() })
      } else {
        await create.mutateAsync(body())
      }
      onClose()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }

  const ready = id.trim() !== '' && (!proxied || proxyEndpoint.trim() !== '')

  return (
    <Modal
      opened
      onClose={onClose}
      title={existing ? '编辑 Egress' : '新建 Egress'}
      size="lg"
      centered
    >
      <Stack gap="sm">
        <TextInput
          label="Egress ID"
          description="Channel 与 Endpoint 用它来引用这条出口。"
          placeholder="egress_proxy_local"
          value={id}
          onChange={(event) => setId(event.currentTarget.value)}
          disabled={existing !== undefined}
          required
        />
        <TextInput
          label="显示名称"
          placeholder="本机代理"
          value={displayName}
          onChange={(event) => setDisplayName(event.currentTarget.value)}
        />
        <Select
          label="出站方式"
          description={egressModeCopy(mode).meaning}
          data={EGRESS_MODES.map((entry) => ({ value: entry.value, label: entry.label }))}
          value={mode}
          onChange={(next) => setMode(next ?? 'direct')}
          required
        />
        {proxied && (
          <>
            <TextInput
              label="代理地址"
              description={
                mode === 'socks5'
                  ? '形如 socks5://127.0.0.1:1080。必须带协议、主机和端口，不能带路径、查询串或用户名密码。'
                  : '形如 http://127.0.0.1:8080。必须带协议、主机和端口，不能带路径、查询串或用户名密码。'
              }
              placeholder={mode === 'socks5' ? 'socks5://127.0.0.1:1080' : 'http://127.0.0.1:8080'}
              value={proxyEndpoint}
              onChange={(event) => setProxyEndpoint(event.currentTarget.value)}
              required
            />
            {existing && (
              <Text fz="xs" c="dimmed">
                Backend 从不把已保存的代理地址回显出来，所以这里读不到旧值。保存会用你现在填的地址整体替换它。
              </Text>
            )}
            {mode === 'socks5' && (
              <Select
                label="域名解析"
                description="决定域名在本机解析还是交给代理解析。"
                data={SOCKS5_DNS_MODES}
                value={socks5Dns}
                onChange={(next) => setSocks5Dns(next ?? 'local')}
                required
              />
            )}
            <Select
              label="Credential"
              description="代理需要 Basic 认证时选一条。留空表示代理不需要认证。"
              placeholder={
                proxyCredentials.length === 0 ? '还没有可用于代理的 Credential' : '选择 Credential'
              }
              data={proxyCredentials.map((entry) => ({
                value: entry.id,
                label: entry.label || entry.id,
              }))}
              value={credentialId}
              onChange={(next) => setCredentialId(next ?? '')}
              disabled={proxyCredentials.length === 0}
              clearable
            />
            <Text fz="xs" c="dimmed">
              代理认证要用一条用途为「代理出口的 Basic 认证」的 Credential。
              <Text span ml={6}>
                <PageLink to="/credentials">前往 Credentials</PageLink>
              </Text>
            </Text>
          </>
        )}
        <Switch
          label="启用"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        {error ? <ErrorAlert error={error} /> : null}

        <Group justify="flex-end" gap="xs" mt="xs">
          <Button variant="default" onClick={onClose}>
            取消
          </Button>
          <Button onClick={() => void submit()} loading={pending} disabled={!ready}>
            {existing ? '保存' : '创建'}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

function DeleteEgressDialog({
  profile,
  opened,
  onClose,
}: {
  profile: EgressProfile
  opened: boolean
  onClose: () => void
}) {
  const remove = useDelete((id) => `/v1/egress-profiles/${id}`, [keys.egress])
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const etag = await readEtag(`/v1/egress-profiles/${profile.id}`)
      await remove.mutateAsync({ id: profile.id, etag })
      onClose()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }

  return (
    <ConfirmDialog
      opened={opened}
      onClose={onClose}
      onConfirm={() => void confirm()}
      title="删除 Egress"
      target={profile.display_name || profile.id}
      consequence="若仍有 Channel 或 Endpoint 绑定这条出口，Backend 会拒绝删除并告知你，不会连带改动它们。"
      loading={pending}
      error={error}
    />
  )
}

/**
 * The provider choice decides which fields exist, because the Backend validates
 * the three families differently — see this file's header.
 */
function CreateEndpointModal({
  egressList,
  onClose,
}: {
  egressList: EgressProfile[]
  onClose: () => void
}) {
  const create = useCreate<EndpointProfileInput, unknown>('/v1/endpoint-profiles', [keys.endpoints])

  const [id, setId] = useState('')
  const [provider, setProvider] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [egressId, setEgressId] = useState('')
  const [trust, setTrust] = useState('configured_endpoint')

  const kind = ENDPOINT_KINDS.find((entry) => entry.provider === provider)
  const enabledEgress = egressList.filter((profile) => profile.enabled)

  const submit = async () => {
    if (!kind) return
    try {
      await create.mutateAsync({
        id: id.trim(),
        provider: kind.provider,
        base_url: kind.officialOrigin ?? baseUrl.trim(),
        egress_profile_id: egressId,
        // Only RSSHub takes a trust value. The Backend fixes `embedding` at
        // `user` and the official providers at `official`, and refuses an
        // override, so sending one would turn a valid write into a refusal.
        trust: kind.provider === 'rsshub' ? trust : undefined,
      })
      onClose()
    } catch {
      // Kept open so the refusal stays next to the fields that caused it.
    }
  }

  const ready =
    id.trim() !== '' &&
    kind !== undefined &&
    egressId !== '' &&
    (kind.officialOrigin !== undefined || baseUrl.trim() !== '')

  return (
    <Modal opened onClose={onClose} title="新建 Endpoint" size="lg" centered>
      <Stack gap="sm">
        <TextInput
          label="Endpoint ID"
          description="创建后不可更改，Channel 与 Semantic Profile 用它来引用这个地址。"
          placeholder="endpoint_rsshub_self"
          value={id}
          onChange={(event) => setId(event.currentTarget.value)}
          required
        />
        <Select
          label="类型"
          description={kind?.description ?? '决定这个 Endpoint 能被谁使用，以及地址怎么校验。'}
          placeholder="选择类型"
          data={ENDPOINT_KINDS.map((entry) => ({ value: entry.provider, label: entry.label }))}
          value={provider}
          onChange={(next) => {
            setProvider(next ?? '')
            setBaseUrl('')
          }}
          required
        />
        {kind && (
          <TextInput
            label="地址"
            description={
              kind.officialOrigin
                ? 'Provider 的官方地址由 Backend 固定，不接受其他值。'
                : kind.urlHint
            }
            value={kind.officialOrigin ?? baseUrl}
            onChange={(event) => setBaseUrl(event.currentTarget.value)}
            disabled={kind.officialOrigin !== undefined}
            required
          />
        )}
        {kind?.provider === 'rsshub' && (
          <Select
            label="来源"
            description="记录这个实例是你自己配置的还是公共实例。"
            data={RSSHUB_TRUST}
            value={trust}
            onChange={(next) => setTrust(next ?? 'configured_endpoint')}
            required
          />
        )}
        <Select
          label="Egress"
          description="这个 Endpoint 固定使用的出口。绑定它的 Channel 会继承这条出口。"
          placeholder={enabledEgress.length === 0 ? '还没有启用的 Egress' : '选择 Egress'}
          data={enabledEgress.map((profile) => ({
            value: profile.id,
            label: profile.display_name || profile.id,
          }))}
          value={egressId}
          onChange={(next) => setEgressId(next ?? '')}
          disabled={enabledEgress.length === 0}
          required
        />

        {create.error ? <ErrorAlert error={create.error} /> : null}

        <Text fz="xs" c="dimmed">
          Endpoint 创建后即为启用状态，Backend 不接受在这里设置启用与否。
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

function DeleteEndpointDialog({
  row,
  opened,
  onClose,
}: {
  row: EndpointProfile
  opened: boolean
  onClose: () => void
}) {
  const remove = useDelete((id) => `/v1/endpoint-profiles/${id}`, [keys.endpoints])
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const etag = await readEtag(`/v1/endpoint-profiles/${row.id}`)
      await remove.mutateAsync({ id: row.id, etag })
      onClose()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }

  return (
    <ConfirmDialog
      opened={opened}
      onClose={onClose}
      onConfirm={() => void confirm()}
      title="删除 Endpoint"
      target={row.id}
      consequence="若仍有 Channel 或 Semantic Profile 引用这个 Endpoint，Backend 会拒绝删除并告知你，不会连带改动它们。"
      loading={pending}
      error={error}
    />
  )
}
