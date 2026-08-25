import { Anchor, Button, Group, Stack, Table, Text } from '@mantine/core'
import { IconArrowLeft, IconRefresh } from '@tabler/icons-react'
import { Link, useParams } from 'react-router'
import { useChannels, useRun, useViews } from '../api/queries'
import type { Item } from '../api/types'
import { RunBadge, Timestamp } from '../components/display'
import { CardRow, EmptyState, ErrorAlert, LoadingRow, PageHeader, SectionCard } from '../components/layout'
import { RUN_KIND_COPY } from './Runs'
import { describeError } from '../domain/vocabulary'

export function RunDetail() {
  const { id } = useParams<{ id: string }>()
  const run = useRun(id)
  const channels = useChannels()
  const views = useViews()

  if (run.isLoading) return <Stack gap="lg"><PageHeader title="活动" /><LoadingRow /></Stack>
  if (!run.data) {
    return (
      <Stack gap="lg">
        <PageHeader title="活动" actions={<Button variant="default" component={Link} to="/activity">返回</Button>} />
        {run.error ? <ErrorAlert error={run.error} onRetry={() => void run.refetch()} /> : <EmptyState title="找不到这条活动记录。" />}
      </Stack>
    )
  }

  const record = run.data
  const channel = (channels.data ?? []).find((entry) => entry.id === record.resource.id)
  const view = (views.data ?? []).find((entry) => entry.view.id === record.resource.id)?.view
  const target = channel?.display_name ?? view?.display_name ?? '已删除的对象'
  const targetPath = record.kind === 'view_refresh'
    ? `/subscriptions/${record.resource.id}`
    : record.kind === 'channel_probe'
      ? `/sources/${record.resource.id}`
      : null
  const lastError = record.last_error ? describeError(record.last_error.code, record.last_error.retryable) : null

  return (
    <Stack gap="lg">
      <PageHeader
        title={RUN_KIND_COPY[record.kind]?.label ?? '活动'}
        description={target}
        actions={
          <>
            <Button variant="default" component={Link} to="/activity" leftSection={<IconArrowLeft size={14} />}>返回</Button>
            <Button variant="default" leftSection={<IconRefresh size={14} />} onClick={() => void run.refetch()} loading={run.isFetching}>刷新</Button>
          </>
        }
      />

      <SectionCard title="结果">
        <CardRow>
          <Stack gap="sm">
            <Group gap="xs"><RunBadge status={record.status} />{targetPath ? <Anchor component={Link} to={targetPath} fz="sm">查看{record.kind === 'view_refresh' ? '订阅' : '来源'}</Anchor> : null}</Group>
            <Text fz="sm" c="dimmed">开始：<Timestamp iso={record.started_at ?? record.created_at} /></Text>
            {record.finished_at ? <Text fz="sm" c="dimmed">完成：<Timestamp iso={record.finished_at} /></Text> : null}
            {record.result ? <Text fz="sm">找到 {record.result.items.length} 条内容，耗时 {record.result.meta.duration_ms} 毫秒。</Text> : null}
            {lastError ? <Text fz="sm" c="red">{lastError.text}{lastError.hint ? `。${lastError.hint}` : ''}</Text> : null}
          </Stack>
        </CardRow>
      </SectionCard>

      {record.result ? (
        <SectionCard title="内容" count={`${record.result.items.length} 条`}>
          {record.result.items.length === 0 ? <EmptyState title="这次没有找到内容。" /> : (
            <Table.ScrollContainer minWidth={620}>
              <Table verticalSpacing="sm" horizontalSpacing="md">
                <Table.Thead><Table.Tr><Table.Th>标题</Table.Th><Table.Th>来源</Table.Th><Table.Th>发布时间</Table.Th></Table.Tr></Table.Thead>
                <Table.Tbody>{record.result.items.map((item) => <ResultRow key={item.id} item={item} />)}</Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </SectionCard>
      ) : null}

      {record.result?.errors.length ? (
        <SectionCard title="未完成的部分">
          {record.result.errors.map((error, index) => {
            const copy = describeError(error.code, error.retryable)
            return <CardRow key={`${error.code}-${index}`}><Text fz="sm">{copy.text}{copy.hint ? `。${copy.hint}` : ''}</Text></CardRow>
          })}
        </SectionCard>
      ) : null}
    </Stack>
  )
}

function ResultRow({ item }: { item: Item }) {
  return (
    <Table.Tr>
      <Table.Td><Anchor href={item.url} target="_blank" rel="noreferrer" fz="sm" fw={500}>{item.title || item.url}</Anchor></Table.Td>
      <Table.Td><Text fz="sm">{item.observations[0]?.source ?? '未知来源'}</Text></Table.Td>
      <Table.Td>{item.published_at ? <Timestamp iso={item.published_at} /> : <Text fz="xs" c="dimmed">未提供</Text>}</Table.Td>
    </Table.Tr>
  )
}
