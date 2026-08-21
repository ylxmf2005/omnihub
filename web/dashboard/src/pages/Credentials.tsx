/**
 * Credentials — the local secret store.
 *
 * The security-bearing decision on this page: a plaintext value exists only
 * inside one component's `useState`, for as long as the user keeps it open. It
 * arrives through `revealCredentialValue`, which is deliberately not a
 * `useQuery`, so it never enters the react-query cache and from there devtools or
 * any future cache serializer. Nothing here writes it to storage, a URL, a log,
 * or the clipboard without an explicit click. The modals that hold a typed secret
 * are mounted only while open, so closing one discards what was in the field.
 *
 * Two contract facts, read off the live Backend, shape the write surface:
 *
 *   **`provider` and `auth_kind` are a closed set of pairs**, not free text.
 *   `POST /v1/credentials` refuses anything outside the list the Backend
 *   supports, so the form offers those pairs instead of two text inputs that
 *   would produce a 400 whenever the user guessed differently.
 *
 *   **A PUT always carries a new plaintext.** The Backend requires `value` on
 *   update, rejects a blank one, and never hands the old value back for us to
 *   replay. Rotating the value is therefore the edit action, and revoke is how a
 *   credential is taken out of service.
 */

import { useEffect, useState } from 'react'
import {
  Button,
  CopyButton,
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
import {
  IconCheck,
  IconCopy,
  IconEye,
  IconEyeOff,
  IconKeyOff,
  IconPlus,
  IconRefresh,
  IconRotate,
  IconTrash,
} from '@tabler/icons-react'
import {
  keys,
  READ_ONLY,
  revealCredentialValue,
  useCreate,
  useCredentials,
  useDelete,
  useRevokeCredential,
  useUpdate,
} from '../api/queries'
import { get } from '../api/client'
import type { Credential, CredentialInput } from '../api/types'
import { IdChip, StateBadge } from '../components/display'
import type { Tone } from '../domain/vocabulary'
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
  SectionCard,
} from '../components/layout'

/**
 * The provider / auth_kind pairs `POST /v1/credentials` accepts, taken from the
 * Backend's own allow-list. `chrome_cookie` is omitted deliberately: it carries
 * no value and is established by the Chrome authorization flow, not by a form.
 */
const CREDENTIAL_KINDS: {
  provider: string
  auth_kind: string
  purpose: string
  valueLabel: string
  valueHint: string
}[] = [
  {
    provider: 'rsshub',
    auth_kind: 'api_key',
    purpose: 'RSSHub 实例的访问密钥',
    valueLabel: '访问密钥',
    valueHint: '你在自己的 RSSHub 部署里配置的访问码。',
  },
  {
    provider: 'github-api',
    auth_kind: 'token',
    purpose: 'GitHub API 的个人访问令牌',
    valueLabel: '令牌',
    valueHint: '在 GitHub 生成的 Personal Access Token。',
  },
  {
    provider: 'tavily',
    auth_kind: 'api_key',
    purpose: 'Tavily 搜索的 API Key',
    valueLabel: 'API Key',
    valueHint: '在 Tavily 控制台生成的 API Key。',
  },
  {
    provider: 'xurl',
    auth_kind: 'app_only',
    purpose: 'xurl 的 app-only 令牌',
    valueLabel: '令牌',
    valueHint: 'xurl 使用的 app-only bearer token。',
  },
  {
    provider: 'embedding',
    auth_kind: 'bearer',
    purpose: 'embedding Endpoint 的 bearer 令牌',
    valueLabel: '令牌',
    valueHint: '向 embedding Endpoint 出示的 bearer token。',
  },
  {
    provider: 'egress',
    auth_kind: 'basic',
    purpose: '代理出口的 Basic 认证',
    valueLabel: '用户名与密码',
    valueHint: '格式为 用户名:密码，两段都不能为空。',
  },
]

function kindKey(provider: string, authKind: string): string {
  return `${provider}/${authKind}`
}

function purposeOf(provider: string, authKind: string): string | undefined {
  if (authKind === 'chrome_cookie') return '由 Chrome 提供的登录 Cookie'
  return CREDENTIAL_KINDS.find(
    (kind) => kind.provider === provider && kind.auth_kind === authKind,
  )?.purpose
}

/**
 * State derived from the two fields the API does report. `chrome_cookie` is
 * separated out because it legitimately has no stored value — for every other
 * kind, a missing value means the credential was revoked.
 */
function credentialState(row: Credential): { label: string; tone: Tone; meaning: string } {
  if (row.auth_kind === 'chrome_cookie') {
    return row.enabled
      ? {
          label: '由 Chrome 提供',
          tone: 'conditional',
          meaning: '值不保存在 OmniHub，每次执行时从 Chrome 取用。',
        }
      : { label: '已停用', tone: 'idle', meaning: '记录仍在，但不会被任何 Channel 使用。' }
  }
  if (!row.has_value) {
    return {
      label: '已撤销',
      tone: 'bad',
      meaning: '存储的值已清空、记录已停用。引用它的 Channel 会明确报告凭据不可用。',
    }
  }
  return row.enabled
    ? { label: '可用', tone: 'ok', meaning: '已存有值，且处于启用状态。' }
    : { label: '已停用', tone: 'idle', meaning: '值仍在，但不会被任何 Channel 使用。' }
}

/**
 * Reads the current detail purely to capture its strong ETag for a following
 * write. Deliberately without `include_value`: a write does not need the
 * plaintext, so it must not fetch it.
 */
async function readEtag(id: string): Promise<string> {
  const current = await get<unknown>(`/v1/credentials/${id}`)
  if (!current.etag) {
    throw new Error('Backend 没有返回 ETag，无法安全写入')
  }
  return current.etag
}

export function Credentials() {
  const credentials = useCredentials()
  const [creating, setCreating] = useState(false)

  const list = credentials.data ?? []

  return (
    <Stack gap="lg">
      <PageHeader
        title="Credentials"
        description="每个 Credential 是一个 API Key 或令牌，Channel 访问它的 Provider 时出示它。"
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              onClick={() => void credentials.refetch()}
              loading={credentials.isFetching}
            >
              刷新
            </Button>
            <Button
              leftSection={<IconPlus size={14} />}
              onClick={() => setCreating(true)}
              disabled={READ_ONLY}
            >
              新建 Credential
            </Button>
          </>
        }
      />

      <DemoModeNotice />
      {credentials.error ? (
        <ErrorAlert error={credentials.error} onRetry={() => void credentials.refetch()} />
      ) : null}

      <SectionCard title="已保存" count={list.length > 0 ? `${list.length} 条` : undefined}>
        {credentials.isLoading ? (
          <LoadingRow />
        ) : list.length === 0 ? (
          <EmptyState
            title="还没有 Credential。"
            hint="Credential 是一个 API Key 或令牌，Channel 在访问它的 Provider 时出示它。只有需要付费接口、私有 RSSHub 实例或需要认证的代理出口时才要配置。"
            action={
              <Button
                leftSection={<IconPlus size={14} />}
                onClick={() => setCreating(true)}
                disabled={READ_ONLY}
              >
                新建 Credential
              </Button>
            }
          />
        ) : (
          <Table.ScrollContainer minWidth={860}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>名称</Table.Th>
                  <Table.Th>用途</Table.Th>
                  <Table.Th>凭据值</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {list.map((row) => (
                  <CredentialTableRow key={row.id} row={row} />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <Caveat>
        凭据值可以在这台机器上显示出来：OmniHub 的信任边界认为，能读取你的 SQLite
        的人本来就能读到这些密钥。但它不会出现在日志、Run 记录、错误信息、readiness 或默认导出里。
      </Caveat>

      {creating && <CreateCredentialModal onClose={() => setCreating(false)} />}
    </Stack>
  )
}

function CredentialTableRow({ row }: { row: Credential }) {
  const [rotating, setRotating] = useState(false)
  const [revoking, setRevoking] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const state = credentialState(row)
  const purpose = purposeOf(row.provider, row.auth_kind)

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <Text fz="sm" fw={500}>
            {row.label || row.id}
          </Text>
          <IdChip value={row.id} width={200} />
        </Table.Td>
        <Table.Td>
          {purpose ? <Text fz="sm">{purpose}</Text> : null}
          <Text fz={10} c="dimmed" ff="monospace">
            {kindKey(row.provider, row.auth_kind)}
          </Text>
        </Table.Td>
        <Table.Td>
          <ValueCell row={row} />
        </Table.Td>
        <Table.Td>
          <StateBadge
            label={state.label}
            tone={state.tone}
            meaning={state.meaning}
            code={`enabled=${row.enabled} has_value=${row.has_value}`}
          />
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="nowrap" justify="flex-end">
            {row.auth_kind !== 'chrome_cookie' && (
              <Button
                variant="default"
                size="compact-sm"
                leftSection={<IconRotate size={13} />}
                disabled={READ_ONLY}
                onClick={() => setRotating(true)}
              >
                轮换
              </Button>
            )}
            {row.has_value && (
              <Button
                variant="default"
                size="compact-sm"
                leftSection={<IconKeyOff size={13} />}
                disabled={READ_ONLY}
                onClick={() => setRevoking(true)}
              >
                撤销
              </Button>
            )}
            <Button
              variant="subtle"
              color="red"
              size="compact-sm"
              px={8}
              disabled={READ_ONLY}
              onClick={() => setDeleting(true)}
              aria-label={`删除 ${row.label || row.id}`}
            >
              <IconTrash size={14} />
            </Button>
          </Group>
        </Table.Td>
      </Table.Tr>

      {rotating && <RotateValueModal row={row} onClose={() => setRotating(false)} />}
      <RevokeCredentialDialog row={row} opened={revoking} onClose={() => setRevoking(false)} />
      <DeleteCredentialDialog row={row} opened={deleting} onClose={() => setDeleting(false)} />
    </>
  )
}

/**
 * Masked by default; revealing is one explicit action whose result lives only
 * here. The value is dropped as soon as the user hides it, and whenever the
 * credential's revision moves — a plaintext read at revision 3 is simply wrong
 * after a rotate, and keeping it on screen would widen the exposure window for
 * nothing.
 */
function ValueCell({ row }: { row: Credential }) {
  const [revealed, setRevealed] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    setRevealed(null)
    setNote(null)
    setError(null)
  }, [row.revision])

  if (!row.has_value) {
    return (
      <Text fz="xs" c="dimmed">
        {row.auth_kind === 'chrome_cookie' ? '不由 OmniHub 保存' : '值已清空'}
      </Text>
    )
  }

  const reveal = async () => {
    setPending(true)
    setNote(null)
    setError(null)
    try {
      const value = await revealCredentialValue(row.id)
      if (value === undefined) {
        setNote('Backend 没有返回值，它可能刚刚被撤销。请刷新列表。')
        return
      }
      setRevealed(value)
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }

  return (
    <Stack gap={4}>
      {revealed === null ? (
        <Group gap="xs" wrap="nowrap">
          <Text fz="xs" ff="monospace" c="dimmed">
            {row.value_masked ?? '已存值'}
          </Text>
          <Button
            variant="subtle"
            size="compact-xs"
            leftSection={<IconEye size={12} />}
            loading={pending}
            onClick={() => void reveal()}
          >
            显示
          </Button>
        </Group>
      ) : (
        <>
          <Text fz="xs" ff="monospace" style={{ wordBreak: 'break-all' }}>
            {revealed}
          </Text>
          <Group gap={4} wrap="nowrap">
            <Button
              variant="subtle"
              size="compact-xs"
              leftSection={<IconEyeOff size={12} />}
              onClick={() => setRevealed(null)}
            >
              隐藏
            </Button>
            <CopyButton value={revealed} timeout={1400}>
              {({ copied, copy }) => (
                <Button
                  variant="subtle"
                  size="compact-xs"
                  leftSection={copied ? <IconCheck size={12} /> : <IconCopy size={12} />}
                  onClick={copy}
                >
                  {copied ? '已复制' : '复制'}
                </Button>
              )}
            </CopyButton>
          </Group>
        </>
      )}
      {note ? (
        <Text fz={10} c="dimmed">
          {note}
        </Text>
      ) : null}
      {error ? <ErrorAlert error={error} /> : null}
    </Stack>
  )
}

/**
 * Rotation, the only update the Backend supports. `label` is omitted from the
 * body on purpose: the Backend rejects a label change outright but accepts an
 * absent one, so echoing the existing label back would turn a valid rotate into
 * a refusal the moment the two strings differed by a space.
 */
function RotateValueModal({ row, onClose }: { row: Credential; onClose: () => void }) {
  const update = useUpdate<CredentialInput, unknown>(
    (id) => `/v1/credentials/${id}`,
    [keys.credentials],
  )
  const [value, setValue] = useState('')
  const [enabled, setEnabled] = useState(row.enabled)
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const kind = CREDENTIAL_KINDS.find(
    (entry) => entry.provider === row.provider && entry.auth_kind === row.auth_kind,
  )

  const submit = async () => {
    setPending(true)
    setError(null)
    try {
      const etag = await readEtag(row.id)
      await update.mutateAsync({
        id: row.id,
        etag,
        body: {
          id: row.id,
          provider: row.provider,
          auth_kind: row.auth_kind,
          value,
          enabled,
        },
      })
      setValue('')
      onClose()
    } catch (caught) {
      setError(caught)
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal opened onClose={onClose} title="轮换凭据值" size="md" centered>
      <Stack gap="sm">
        <Text fz="sm">
          将要更新：
          <Text span ff="monospace" fz="sm" ml={6}>
            {row.id}
          </Text>
        </Text>
        <PasswordInput
          label={kind?.valueLabel ?? '新的凭据值'}
          description={kind?.valueHint}
          value={value}
          onChange={(event) => setValue(event.currentTarget.value)}
          autoComplete="off"
          required
        />
        <Switch
          label="更新后保持启用"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />
        <Text fz="xs" c="dimmed">
          Backend 要求每次更新都带一个新值，也不会把旧值交回来给我们重放，所以轮换必须重新填写完整的值。
          Provider、用途与备注创建后不可更改。
        </Text>

        {error ? <ErrorAlert error={error} /> : null}

        <Group justify="flex-end" gap="xs" mt="xs">
          <Button variant="default" onClick={onClose}>
            取消
          </Button>
          <Button onClick={() => void submit()} loading={pending} disabled={value === ''}>
            更新
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

/**
 * Revocation and deletion are different operations and the copy has to say so:
 * revoking clears the stored value but keeps the ID, so a Channel that still
 * references it reports a blocked credential instead of failing obscurely.
 */
function RevokeCredentialDialog({
  row,
  opened,
  onClose,
}: {
  row: Credential
  opened: boolean
  onClose: () => void
}) {
  const revoke = useRevokeCredential()
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const etag = await readEtag(row.id)
      await revoke.mutateAsync({ id: row.id, etag })
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
      title="撤销 Credential"
      target={row.label || row.id}
      consequence="撤销会清空存储的值并停用这条记录，但保留它的 ID：仍引用它的 Channel 会明确报告凭据不可用。已清空的值取不回来，要恢复可用需要重新填入一个新值。"
      confirmLabel="撤销"
      loading={pending}
      error={error}
    />
  )
}

function DeleteCredentialDialog({
  row,
  opened,
  onClose,
}: {
  row: Credential
  opened: boolean
  onClose: () => void
}) {
  const remove = useDelete((id) => `/v1/credentials/${id}`, [keys.credentials])
  const [error, setError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const confirm = async () => {
    setPending(true)
    setError(null)
    try {
      const etag = await readEtag(row.id)
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
      title="删除 Credential"
      target={row.label || row.id}
      consequence="删除会移除整条记录，包括它的 ID。若仍有 Channel 引用它，Backend 会拒绝删除并告知你，不会连带改动那些 Channel。只是想让它不再可用的话，用撤销。"
      loading={pending}
      error={error}
    />
  )
}

function CreateCredentialModal({ onClose }: { onClose: () => void }) {
  const create = useCreate<CredentialInput, unknown>('/v1/credentials', [keys.credentials])

  const [id, setId] = useState('')
  const [label, setLabel] = useState('')
  const [selectedKind, setSelectedKind] = useState('')
  const [value, setValue] = useState('')
  const [enabled, setEnabled] = useState(true)

  const kind = CREDENTIAL_KINDS.find(
    (entry) => kindKey(entry.provider, entry.auth_kind) === selectedKind,
  )

  const submit = async () => {
    if (!kind) return
    try {
      await create.mutateAsync({
        id: id.trim(),
        provider: kind.provider,
        auth_kind: kind.auth_kind,
        label: label.trim() || undefined,
        value,
        enabled,
      })
      setValue('')
      onClose()
    } catch {
      // Kept open so the refusal stays next to the fields that caused it.
    }
  }

  const ready = id.trim() !== '' && kind !== undefined && value !== ''

  return (
    <Modal opened onClose={onClose} title="新建 Credential" size="lg" centered>
      <Stack gap="sm">
        <TextInput
          label="Credential ID"
          description="创建后不可更改，Channel 与 Egress 用它来引用这条凭据。"
          placeholder="cred_rsshub_self"
          value={id}
          onChange={(event) => setId(event.currentTarget.value)}
          required
        />
        <TextInput
          label="备注"
          description="给自己看的名字。创建后不可修改。"
          placeholder="自建 RSSHub"
          value={label}
          onChange={(event) => setLabel(event.currentTarget.value)}
        />
        <Select
          label="用途"
          description="决定这条凭据能被哪个 Provider 使用。Backend 只接受下列组合。"
          placeholder="选择用途"
          data={CREDENTIAL_KINDS.map((entry) => ({
            value: kindKey(entry.provider, entry.auth_kind),
            label: entry.purpose,
          }))}
          value={selectedKind}
          onChange={(next) => {
            setSelectedKind(next ?? '')
            setValue('')
          }}
          required
        />
        {kind && (
          <>
            <PasswordInput
              label={kind.valueLabel}
              description={kind.valueHint}
              value={value}
              onChange={(event) => setValue(event.currentTarget.value)}
              autoComplete="off"
              required
            />
            <MetaLine>
              <Fact label="Provider">
                <Text span fz="xs" ff="monospace">
                  {kind.provider}
                </Text>
              </Fact>
              <Fact label="认证方式">
                <Text span fz="xs" ff="monospace">
                  {kind.auth_kind}
                </Text>
              </Fact>
            </MetaLine>
          </>
        )}
        <Switch
          label="创建后立即启用"
          checked={enabled}
          onChange={(event) => setEnabled(event.currentTarget.checked)}
        />

        {create.error ? <ErrorAlert error={create.error} /> : null}

        <Text fz="xs" c="dimmed">
          值只写入这台机器的 SQLite。它不会进入日志、Run 记录、错误信息或默认导出，
          之后也只能通过这个页面上的显式操作再看到。
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
