/**
 * Collections — grouping Channels so one Operation can scope to a set of sources.
 *
 * The shapes here were learned by creating a throwaway Collection against the live
 * Backend, because the list is empty on a fresh instance and an empty array teaches
 * nothing: the display field is `title` (not `display_name`) and `position` is
 * required on both POST and PUT. types.ts now carries that verified shape, so this
 * page reads `Collection` and `CollectionInput` directly.
 *
 * A View scopes to a Collection through `operation.scope.collection`, which the live
 * schema types as a single string — one Collection per Operation, not a list. The
 * empty-state copy says exactly that and nothing more.
 *
 * Collections do nest (`parent_id`), but nesting does not affect scope: the router's
 * `collectionSet` resolves an Operation's `scope.collection` from that Collection's own
 * `channel_ids` only and never walks children. So a flat list does not misstate what a
 * scoped Operation will execute, and this page presents one rather than a tree. What
 * nesting does affect is deletion — a parent holding a child is refused as
 * `resource_in_use` — which the delete copy names.
 *
 * `position` is a client-side ordering hint: the API returns Collections in id order
 * regardless of it, so the sort below is what makes the field mean anything.
 */

import { useEffect, useState } from 'react'
import {
  Badge,
  Box,
  Button,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPencil, IconPlus, IconRefresh, IconTrash } from '@tabler/icons-react'
import {
  keys,
  READ_ONLY,
  useChannels,
  useCollections,
  useCreate,
  useDelete,
  useUpdate,
} from '../api/queries'
import { get } from '../api/client'
import type { Collection, CollectionInput } from '../api/types'
import { IdChip } from '../components/display'
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

export function Collections() {
  const collections = useCollections()
  const channels = useChannels()
  const [creating, setCreating] = useState(false)

  const list = collections.data ?? []
  const nameOf = new Map(
    (channels.data ?? []).map((channel) => [channel.id, channel.display_name || channel.id]),
  )

  const ordered = [...list].sort((left, right) => left.position - right.position)

  return (
    <Stack gap="lg">
      <PageHeader
        title="Collections"
        description="把若干 Channel 归成一组，让一个 Operation 一次性把范围指向整组来源，而不用逐条列出。"
        actions={
          <>
            <Button
              variant="default"
              leftSection={<IconRefresh size={14} />}
              onClick={() => void collections.refetch()}
              loading={collections.isFetching}
            >
              刷新
            </Button>
            <Button
              leftSection={<IconPlus size={14} />}
              onClick={() => setCreating(true)}
              disabled={READ_ONLY}
            >
              新建 Collection
            </Button>
          </>
        }
      />

      <DemoModeNotice />
      {collections.error ? (
        <ErrorAlert error={collections.error} onRetry={() => void collections.refetch()} />
      ) : null}

      <SectionCard title="已配置" count={ordered.length > 0 ? `${ordered.length} 个` : undefined}>
        {collections.isLoading ? (
          <LoadingRow />
        ) : ordered.length === 0 ? (
          <EmptyState
            title="还没有 Collection。"
            hint="Collection 是一组 Channel 的名字。把几条常一起用的线路归到一组之后，新建 View 时可以直接把 Operation 的范围指向这个 Collection；往组里增删 Channel 就会同时改变所有指向它的 View 的取数范围，不用逐个改。一个 Operation 只能指向一个 Collection。"
            action={
              <Button
                leftSection={<IconPlus size={14} />}
                onClick={() => setCreating(true)}
                disabled={READ_ONLY}
              >
                新建 Collection
              </Button>
            }
          />
        ) : (
          <Table.ScrollContainer minWidth={720}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>名称</Table.Th>
                  <Table.Th>包含的 Channel</Table.Th>
                  <Table.Th>状态</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {ordered.map((collection) => (
                  <CollectionRow key={collection.id} collection={collection} nameOf={nameOf} />
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <CollectionModal opened={creating} onClose={() => setCreating(false)} />
    </Stack>
  )
}

function CollectionRow({
  collection,
  nameOf,
}: {
  collection: Collection
  nameOf: Map<string, string>
}) {
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const members = collection.channel_ids

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <Text fz="sm" fw={500}>
            {collection.title || collection.id}
          </Text>
          <Box mt={2}>
            <IdChip value={collection.id} width={210} />
          </Box>
        </Table.Td>
        <Table.Td>
          {members.length === 0 ? (
            <Text fz="sm" c="dimmed">
              还没有加入任何 Channel
            </Text>
          ) : (
            <Stack gap={2}>
              {members.map((channelId) => (
                <Text key={channelId} fz="sm">
                  {nameOf.get(channelId) ?? channelId}
                </Text>
              ))}
            </Stack>
          )}
        </Table.Td>
        <Table.Td>
          <Group gap="xs" wrap="wrap">
            {collection.enabled ? (
              <Badge variant="light" color="teal" size="xs" styles={{ label: { fontWeight: 500 } }}>
                已启用
              </Badge>
            ) : (
              <Badge variant="default" size="xs" styles={{ label: { fontWeight: 400 } }}>
                已停用
              </Badge>
            )}
          </Group>
          <MetaLine gap="sm">
            <Fact label="排序">{collection.position}</Fact>
          </MetaLine>
        </Table.Td>
        <Table.Td>
          <Group gap={4} wrap="nowrap" justify="flex-end">
            <Button
              variant="default"
              size="compact-sm"
              leftSection={<IconPencil size={13} />}
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
              aria-label={`删除 ${collection.title || collection.id}`}
            >
              <IconTrash size={14} />
            </Button>
          </Group>
        </Table.Td>
      </Table.Tr>

      <CollectionModal
        existing={collection}
        opened={editing}
        onClose={() => setEditing(false)}
      />
      <DeleteCollectionDialog
        collection={collection}
        opened={deleting}
        onClose={() => setDeleting(false)}
      />
    </>
  )
}

/**
 * Deletion in two steps: read the detail route for its strong ETag, hold it across
 * the confirmation, then send that exact validator.
 *
 * The Backend deletes only an *empty* Collection: `DeleteCollection` refuses one
 * that still lists Channels or has a child Collection, as `resource_in_use`. The
 * consequence copy names that precondition, because "remove the Channels first" is
 * the actual next step and the error alone does not say which of the two it was.
 */
function DeleteCollectionDialog({
  collection,
  opened,
  onClose,
}: {
  collection: Collection
  opened: boolean
  onClose: () => void
}) {
  const remove = useDelete((id) => `/v1/collections/${id}`, [keys.collections])
  const [etagError, setEtagError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  const members = collection.channel_ids

  const confirm = async () => {
    setPending(true)
    setEtagError(null)
    try {
      const current = await get<Collection>(`/v1/collections/${collection.id}`)
      if (!current.etag) {
        throw new Error('Backend 没有返回 ETag，无法安全删除')
      }
      await remove.mutateAsync({ id: collection.id, etag: current.etag })
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
      title="删除 Collection"
      target={collection.title || collection.id}
      consequence={
        members.length > 0
          ? `这个分组里还有 ${members.length} 个 Channel。Backend 只删除空分组，需要先把它们从组里移出去，再删这个分组。Channel 本身不会被删掉。`
          : '只删掉这个分组，组里的 Channel 本身不受影响。如果它还有下级分组，Backend 会拒绝删除并告知你。'
      }
      loading={pending}
      error={etagError ?? remove.error}
    />
  )
}

/**
 * One form for create and edit. Editing sends a full PUT with the ETag captured by
 * a fresh detail read at submit time — the list response carries `revision` but not
 * a validator, and building one from `revision` is exactly what the ETag rules
 * forbid. A `409 revision_conflict` surfaces and is never retried.
 */
function CollectionModal({
  existing,
  opened,
  onClose,
}: {
  existing?: Collection
  opened: boolean
  onClose: () => void
}) {
  const create = useCreate<CollectionInput, Collection>('/v1/collections', [keys.collections])
  const update = useUpdate<CollectionInput, Collection>(
    (id) => `/v1/collections/${id}`,
    [keys.collections],
  )
  const channels = useChannels()

  const [id, setId] = useState('')
  const [title, setTitle] = useState('')
  const [position, setPosition] = useState<number>(0)
  const [channelIds, setChannelIds] = useState<string[]>([])
  const [enabled, setEnabled] = useState(true)
  const [etagError, setEtagError] = useState<unknown>(null)
  const [pending, setPending] = useState(false)

  // Re-seed whenever the dialog opens so an abandoned edit cannot leak forward.
  useEffect(() => {
    if (!opened) return
    setId(existing?.id ?? '')
    setTitle(existing?.title ?? '')
    setPosition(existing?.position ?? 0)
    setChannelIds(existing?.channel_ids ?? [])
    setEnabled(existing?.enabled ?? true)
    setEtagError(null)
    create.reset()
    update.reset()
  }, [opened, existing, create.reset, update.reset])

  const submit = async () => {
    const body: CollectionInput = {
      id: id.trim(),
      title: title.trim(),
      position,
      channel_ids: channelIds,
      enabled,
    }

    setPending(true)
    setEtagError(null)
    try {
      if (existing) {
        const current = await get<Collection>(`/v1/collections/${existing.id}`)
        if (!current.etag) {
          throw new Error('Backend 没有返回 ETag，无法安全提交')
        }
        await update.mutateAsync({ id: existing.id, etag: current.etag, body })
      } else {
        await create.mutateAsync(body)
      }
      onClose()
    } catch (error) {
      setEtagError(error)
    } finally {
      setPending(false)
    }
  }

  const ready = id.trim() !== '' && title.trim() !== ''

  return (
    <Modal
      opened={opened}
      onClose={onClose}
      title={existing ? '编辑 Collection' : '新建 Collection'}
      size="lg"
      centered
    >
      <Stack gap="sm">
        <TextInput
          label="Collection ID"
          description={
            existing
              ? '创建后不可更改。'
              : '创建后不可更改，Operation 用它来指向这个分组。'
          }
          placeholder="col_daily_reading"
          value={id}
          onChange={(event) => setId(event.currentTarget.value)}
          disabled={Boolean(existing)}
          required
        />
        <TextInput
          label="名称"
          placeholder="每日跟读"
          value={title}
          onChange={(event) => setTitle(event.currentTarget.value)}
          required
        />
        <MultiSelect
          label="包含的 Channel"
          description="可以先留空，之后再往组里加。"
          placeholder={channels.isLoading ? '正在读取…' : '选择 Channel'}
          data={(channels.data ?? []).map((channel) => ({
            value: channel.id,
            label: channel.display_name || channel.id,
          }))}
          value={channelIds}
          onChange={setChannelIds}
          searchable
        />
        <Group grow align="flex-start">
          <NumberInput
            label="排序"
            description="决定这个分组在列表里的先后，数值小的在前。"
            value={position}
            onChange={(value) => setPosition(typeof value === 'number' ? value : 0)}
            min={0}
          />
          <Box pt={26}>
            <Switch
              label="启用"
              checked={enabled}
              onChange={(event) => setEnabled(event.currentTarget.checked)}
            />
          </Box>
        </Group>

        {etagError ? <ErrorAlert error={etagError} /> : null}

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
