/**
 * Semantic Profiles — the embedding cohort behind `similarity_grouping`.
 *
 * A profile freezes the parameters one batch of vectors was computed with: which
 * embedding Endpoint, which model, how many dimensions, and how close two items
 * must be to count as the same story. `index_revision` is the part that is easy
 * to misread as bookkeeping — it is the cohort marker. Vectors computed under
 * revision 2 are never compared against revision 1, so changing the model or the
 * dimension without raising it would silently mix incompatible vectors. The form
 * therefore raises it on the user's behalf when the recipe changes, and says so.
 *
 * Contract facts verified against the live Backend:
 *
 *   **The Endpoint must be an enabled `embedding` Endpoint** whose own Egress is
 *   enabled. Any other provider is refused, so the picker only offers embedding
 *   Endpoints rather than letting the user discover this through a 400.
 *
 *   **A credential is optional but constrained**: only an `embedding` / `bearer`
 *   Credential, and only when the Endpoint is HTTPS.
 *
 *   **A profile has no name of its own.** There is no `display_name` field, so
 *   the identity a user recognises is the model plus the Endpoint it runs on, and
 *   that is what the list leads with.
 */

import { useState } from 'react'
import {
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
import { IconPlus, IconRefresh, IconTrash } from '@tabler/icons-react'
import {
  keys,
  READ_ONLY,
  useCreate,
  useCredentials,
  useDelete,
  useEndpointProfiles,
  useSemanticProfiles,
  useUpdate,
} from '../api/queries'
import { get } from '../api/client'
import type {
  Credential,
  EndpointProfile,
  SemanticProfile,
  SemanticProfileInput,
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

/** Reads a resource purely to capture its strong ETag for a following write. */
async function readEtag(path: string): Promise<string> {
  const current = await get<unknown>(path)
  if (!current.etag) {
    throw new Error('Backend 没有返回 ETag，无法安全写入')
  }
  return current.etag
}

export function SemanticProfiles() {
  const profiles = useSemanticProfiles()
  const endpoints = useEndpointProfiles()
  const [creating, setCreating] = useState(false)

  const list = profiles.data ?? []
  const embeddingEndpoints = (endpoints.data ?? []).filter(
    (entry: EndpointProfile) => entry.provider === 'embedding' && entry.enabled,
  )

  return (
    <Stack gap="lg">
      <PageHeader
        title="Semantic Profiles"
        description="一个 Semantic Profile 固定一次语义分组用的 embedding 参数：用哪个 Endpoint、哪个模型、多少维，以及多近算同一件事。"
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              onClick={() => {
                void profiles.refetch()
                void endpoints.refetch()
              }}
              loading={profiles.isFetching}
            >
              刷新
            </Button>
            <Button
              leftSection={<IconPlus size={14} />}
              onClick={() => setCreating(true)}
              disabled={READ_ONLY || embeddingEndpoints.length === 0}
            >
              新建 Semantic Profile
            </Button>
          </>
        }
      />

      <DemoModeNotice />
      {profiles.error ? (
        <ErrorAlert error={profiles.error} onRetry={() => void profiles.refetch()} />
      ) : null}

      <SectionCard title="已配置" count={list.length > 0 ? `${list.length} 个` : undefined}>
        {profiles.isLoading ? (
          <LoadingRow />
        ) : list.length === 0 ? (
          <EmptyState
            title="还没有 Semantic Profile。"
            hint="当 Operation 把 similarity_grouping 设为 semantic 时需要一个 Semantic Profile：它决定用哪个 embedding 模型判断两条内容讲的是不是同一件事。"
            action={
              embeddingEndpoints.length === 0 ? (
                <Text fz="xs" c="dimmed">
                  需要先有一个启用的 embedding Endpoint。
                  <Text span ml={6}>
                    <PageLink to="/connections">前往 Connections</PageLink>
                  </Text>
                </Text>
              ) : (
                <Button
                  leftSection={<IconPlus size={14} />}
                  onClick={() => setCreating(true)}
                  disabled={READ_ONLY}
                >
                  新建 Semantic Profile
                </Button>
              )
            }
          />
        ) : (
          <Table.ScrollContainer minWidth={900}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>模型</Table.Th>
                  <Table.Th>Endpoint</Table.Th>
                  <Table.Th>分组阈值</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {list.map((row) => (
                  <SemanticTableRow key={row.id} row={row} />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <Caveat>
        embedding Endpoint 不可达时，查询不会因此丢结果：它仍然返回全部内容，只是不做语义分组，
        并在结果里附带一条 Semantic 分组不可用的说明。
      </Caveat>

      {creating && (
        <SemanticModal endpoints={embeddingEndpoints} onClose={() => setCreating(false)} />
      )}
    </Stack>
  )
}

function SemanticTableRow({ row }: { row: SemanticProfile }) {
  const endpoints = useEndpointProfiles()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const embeddingEndpoints = (endpoints.data ?? []).filter(
    (entry: EndpointProfile) => entry.provider === 'embedding' && entry.enabled,
  )
  const endpoint = (endpoints.data ?? []).find(
    (entry) => entry.id === row.endpoint_profile_id,
  )

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <Text fz="sm" fw={500}>
            {row.model}
          </Text>
          <IdChip value={row.id} width={200} />
        </Table.Td>
        <Table.Td>
          <Text fz="sm">{endpoint?.base_url ?? row.endpoint_profile_id}</Text>
          <MetaLine gap="sm">
            <Fact label="维度">{row.dimension}</Fact>
            {row.credential_id && <Fact label="Credential">{row.credential_id}</Fact>}
          </MetaLine>
        </Table.Td>
        <Table.Td>
          <Text fz="sm" ff="monospace">
            {row.threshold}
          </Text>
          <Text fz="xs" c="dimmed">
            相似度达到它才归为一组
          </Text>
        </Table.Td>
        <Table.Td>
          {row.enabled ? (
            <StateBadge
              label="启用"
              tone="ok"
              meaning="Operation 可以引用它做语义分组。"
              code="enabled=true"
            />
          ) : (
            <StateBadge
              label="已停用"
              tone="idle"
              meaning="配置仍在，但引用它的查询不会做语义分组。"
              code="enabled=false"
            />
          )}
          <MetaLine gap="sm">
            <Fact label="向量批次">{row.index_revision}</Fact>
          </MetaLine>
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
              aria-label={`删除 ${row.id}`}
            >
              <IconTrash size={14} />
            </Button>
          </Group>
        </Table.Td>
      </Table.Tr>

      {editing && (
        <SemanticModal
          existing={row}
          endpoints={embeddingEndpoints}
          onClose={() => setEditing(false)}
        />
      )}
      <DeleteSemanticDialog row={row} opened={deleting} onClose={() => setDeleting(false)} />
    </>
  )
}

/**
 * One modal for create and edit, since PUT is a full replacement.
 *
 * The judgment worth naming: when an edit changes the model, the dimension or
 * the Endpoint, `index_revision` is raised automatically. Those three inputs are
 * what a vector depends on, so leaving the cohort marker alone would let the
 * Backend compare vectors that were never comparable. Changing only the
 * threshold leaves it untouched — a threshold is applied at query time and does
 * not invalidate anything already computed.
 */
function SemanticModal({
  existing,
  endpoints,
  onClose,
}: {
  existing?: SemanticProfile
  endpoints: EndpointProfile[]
  onClose: () => void
}) {
  const create = useCreate<SemanticProfileInput, unknown>('/v1/semantic-profiles', [keys.semantic])
  const update = useUpdate<SemanticProfileInput, unknown>(
    (id) => `/v1/semantic-profiles/${id}`,
    [keys.semantic],
  )
  const credentials = useCredentials()

  const [id, setId] = useState(existing?.id ?? '')
  const [endpointId, setEndpointId] = useState(existing?.endpoint_profile_id ?? '')
  const [model, setModel] = useState(existing?.model ?? '')
  const [dimension, setDimension] = useState<number>(existing?.dimension ?? 1536)
  const [threshold, setThreshold] = useState<number>(existing?.threshold ?? 0.82)
  const [credentialId, setCredentialId] = useState(existing?.credential_id ?? '')
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  // Only an embedding/bearer credential that still holds a value can authenticate
  // an embedding Endpoint; anything else is refused by the Backend.
  const bearerCredentials = (credentials.data ?? []).filter(
    (entry: Credential) =>
      entry.provider === 'embedding' &&
      entry.auth_kind === 'bearer' &&
      entry.enabled &&
      entry.has_value,
  )

  const recipeChanged =
    existing !== undefined &&
    (model.trim() !== existing.model ||
      dimension !== existing.dimension ||
      endpointId !== existing.endpoint_profile_id)

  const indexRevision =
    existing === undefined ? 1 : recipeChanged ? existing.index_revision + 1 : existing.index_revision

  const submit = async () => {
    setPending(true)
    setError(null)
    try {
      const body: SemanticProfileInput = {
        id: existing?.id ?? id.trim(),
        endpoint_profile_id: endpointId,
        model: model.trim(),
        dimension,
        threshold,
        index_revision: indexRevision,
        enabled,
        credential_id: credentialId || undefined,
      }
      if (existing) {
        const etag = await readEtag(`/v1/semantic-profiles/${existing.id}`)
        await update.mutateAsync({ id: existing.id, etag, body })
      } else {
        await create.mutateAsync(body)
      }
      onClose()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }

  const ready = id.trim() !== '' && endpointId !== '' && model.trim() !== ''

  return (
    <Modal
      opened
      onClose={onClose}
      title={existing ? '编辑 Semantic Profile' : '新建 Semantic Profile'}
      size="lg"
      centered
    >
      <Stack gap="sm">
        <TextInput
          label="Profile ID"
          description="Operation 用它来引用这组语义分组参数。"
          placeholder="semantic_openai_small"
          value={id}
          onChange={(event) => setId(event.currentTarget.value)}
          disabled={existing !== undefined}
          required
        />
        <Select
          label="embedding Endpoint"
          description="必须是一个启用的 embedding Endpoint，且它绑定的 Egress 也处于启用状态。"
          placeholder={endpoints.length === 0 ? '还没有可用的 embedding Endpoint' : '选择 Endpoint'}
          data={endpoints.map((entry) => ({
            value: entry.id,
            label: entry.base_url ? `${entry.id}（${entry.base_url}）` : entry.id,
          }))}
          value={endpointId}
          onChange={(next) => setEndpointId(next ?? '')}
          disabled={endpoints.length === 0}
          required
        />
        <TextInput
          label="模型"
          description="Endpoint 上用来计算向量的模型名。"
          placeholder="text-embedding-3-small"
          value={model}
          onChange={(event) => setModel(event.currentTarget.value)}
          required
        />
        <Group grow align="flex-start">
          <NumberInput
            label="维度"
            description="要与模型实际输出的维度一致。"
            value={dimension}
            onChange={(value) => setDimension(typeof value === 'number' ? value : 0)}
            min={1}
            max={16384}
            required
          />
          <NumberInput
            label="分组阈值"
            description="相似度达到它才归为一组。取值大于 0 且不超过 1。"
            value={threshold}
            onChange={(value) => setThreshold(typeof value === 'number' ? value : 0)}
            min={0.01}
            max={1}
            step={0.01}
            decimalScale={2}
            required
          />
        </Group>
        <Select
          label="Credential"
          description="Endpoint 需要 bearer 令牌时选一条。留空表示不需要认证。"
          placeholder={
            bearerCredentials.length === 0 ? '还没有可用于 embedding 的 Credential' : '选择 Credential'
          }
          data={bearerCredentials.map((entry) => ({
            value: entry.id,
            label: entry.label || entry.id,
          }))}
          value={credentialId}
          onChange={(next) => setCredentialId(next ?? '')}
          disabled={bearerCredentials.length === 0}
          clearable
        />
        <Text fz="xs" c="dimmed">
          认证要用一条用途为「embedding Endpoint 的 bearer 令牌」的 Credential，且 Endpoint 必须是 HTTPS。
          <Text span ml={6}>
            <PageLink to="/credentials">前往 Credentials</PageLink>
          </Text>
        </Text>
        <Switch
          label="启用"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        {recipeChanged && (
          <Text fz="xs" c="dimmed">
            模型、维度或 Endpoint 变了，向量批次会从 {existing?.index_revision} 提升到 {indexRevision}。
            旧向量不会再与新批次一起比较，之前算过的内容需要重新计算。
          </Text>
        )}

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

function DeleteSemanticDialog({
  row,
  opened,
  onClose,
}: {
  row: SemanticProfile
  opened: boolean
  onClose: () => void
}) {
  const remove = useDelete((id) => `/v1/semantic-profiles/${id}`, [keys.semantic])
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const etag = await readEtag(`/v1/semantic-profiles/${row.id}`)
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
      title="删除 Semantic Profile"
      target={row.id}
      consequence="若仍有 View 的 Operation 引用它做语义分组，Backend 会拒绝删除并告知你，不会连带改动那些 View。"
      loading={pending}
      error={error}
    />
  )
}
