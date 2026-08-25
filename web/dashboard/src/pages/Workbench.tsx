import {
  Anchor,
  Badge,
  Button,
  Group,
  MultiSelect,
  Select,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
} from '@mantine/core'
import { IconSearch } from '@tabler/icons-react'
import { useEffect, useMemo, useState } from 'react'
import { useChannels, useRouteTemplates, useRunQuery } from '../api/queries'
import type { Envelope, Item, SearchInput, SearchSort } from '../api/types'
import { CardRow, EmptyState, ErrorAlert, PageHeader, SectionCard } from '../components/layout'

export function Workbench() {
  const channels = useChannels()
  const templates = useRouteTemplates()
  const search = useRunQuery('search')
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [initialized, setInitialized] = useState(false)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [sort, setSort] = useState<SearchSort>('relevance')
  const [authors, setAuthors] = useState('')
  const [categories, setCategories] = useState('')
  const [tags, setTags] = useState('')
  const [pageSize, setPageSize] = useState(20)
  const [pages, setPages] = useState<Envelope[]>([])
  const [pageIndex, setPageIndex] = useState(0)

  const eligible = useMemo(() => {
    const capabilities = new Map(
      (templates.data ?? []).map((template) => [template.route_template_id, template.capabilities]),
    )
    return (channels.data ?? []).filter(
      (channel) => channel.enabled && capabilities.get(channel.route_template_id)?.includes('search'),
    )
  }, [channels.data, templates.data])

  useEffect(() => {
    if (!initialized && eligible.length > 0) {
      setSelected(eligible.map((channel) => channel.id))
      setInitialized(true)
    }
  }, [eligible, initialized])

  useEffect(() => {
    setPages([])
    setPageIndex(0)
    search.reset()
  }, [query, selected, from, to, sort, authors, categories, tags, pageSize])

  const split = (value: string) => value.split(',').map((part) => part.trim()).filter(Boolean)
  const request = (continuation?: string): SearchInput => {
    const time = {
      ...(from ? { from: new Date(from).toISOString() } : {}),
      ...(to ? { to: new Date(to).toISOString() } : {}),
    }
    return {
      schema_version: '1.0',
      query: query.trim(),
      scope: { channels: selected },
      route_policy: {
        mode: 'only',
        only: selected.map((id) => ({ kind: 'channel', id })),
        aggregate: true,
        allow_fallback: false,
      },
      limit: pageSize,
      constraints: {
        ...(Object.keys(time).length > 0 ? { time: { field: 'published_at' as const, ...time } } : {}),
        ...(split(authors).length > 0 ? { authors: split(authors) } : {}),
        ...(split(categories).length > 0 ? { categories: split(categories) } : {}),
        ...(split(tags).length > 0 ? { tags: split(tags) } : {}),
      },
      sort,
      identity_dedupe: 'exact',
      similarity_grouping: 'off',
      deadline_ms: 30_000,
      ...(continuation ? { continuation } : {}),
    }
  }

  const run = () => {
    search.mutate(request(), {
      onSuccess: (result) => {
        setPages([result])
        setPageIndex(0)
      },
    })
  }

  const next = () => {
    if (pageIndex + 1 < pages.length) {
      setPageIndex(pageIndex + 1)
      return
    }
    const token = pages[pageIndex]?.continuation.token
    if (!token) return
    search.mutate(request(token), {
      onSuccess: (result) => {
        setPages((current) => [...current, result])
        setPageIndex((current) => current + 1)
      },
    })
  }

  const missing = query.trim() === '' || selected.length === 0
  const current = pages[pageIndex]
  const hasNext = pageIndex + 1 < pages.length || Boolean(current?.continuation.token)

  return (
    <Stack gap="lg">
      <PageHeader title="搜索" description="从已连接的来源中查找内容。" />

      <SectionCard title="搜索条件">
        <CardRow>
          <Stack gap="md">
            <TextInput
              label="关键词"
              placeholder="输入要查找的内容"
              value={query}
              onChange={(event) => setQuery(event.currentTarget.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && !missing) run()
              }}
              required
            />
            <MultiSelect
              label="来源"
              placeholder={eligible.length === 0 ? '还没有支持搜索的来源' : '选择来源'}
              data={eligible.map((channel) => ({
                value: channel.id,
                label: channel.display_name ?? channel.source,
              }))}
              value={selected}
              onChange={setSelected}
              searchable
              required
            />
            <SimpleGrid cols={{ base: 1, sm: 3 }} spacing="sm">
              <TextInput type="datetime-local" label="发布时间从" value={from} onChange={(event) => setFrom(event.currentTarget.value)} />
              <TextInput type="datetime-local" label="发布时间到" value={to} onChange={(event) => setTo(event.currentTarget.value)} />
              <Select
                label="排序"
                data={[{ value: 'relevance', label: '相关度' }, { value: 'newest', label: '最新优先' }]}
                value={sort}
                onChange={(value) => setSort((value as SearchSort) ?? 'relevance')}
                allowDeselect={false}
              />
            </SimpleGrid>
            <details>
              <summary>更多筛选</summary>
              <Stack gap="sm" mt="sm">
                <TextInput label="作者" description="多个作者用逗号分隔" value={authors} onChange={(event) => setAuthors(event.currentTarget.value)} />
                <TextInput label="分类" description="多个分类用逗号分隔" value={categories} onChange={(event) => setCategories(event.currentTarget.value)} />
                <TextInput label="标签" description="多个标签用逗号分隔" value={tags} onChange={(event) => setTags(event.currentTarget.value)} />
              </Stack>
            </details>
            <Group justify="space-between" align="flex-end">
              <Select
                label="每页"
                data={['10', '20', '50']}
                value={String(pageSize)}
                onChange={(value) => setPageSize(Number(value ?? 20))}
                allowDeselect={false}
                w={110}
              />
              <Button leftSection={<IconSearch size={15} />} onClick={run} loading={search.isPending} disabled={missing}>
                搜索
              </Button>
            </Group>
          </Stack>
        </CardRow>
      </SectionCard>

      {search.error ? <ErrorAlert error={search.error} onRetry={pages.length > 0 ? next : run} /> : null}
      <SearchResults items={current?.items} failed={current?.errors.length ?? 0} />
      {current ? (
        <Group justify="center">
          <Button variant="default" disabled={pageIndex === 0 || search.isPending} onClick={() => setPageIndex(pageIndex - 1)}>
            上一页
          </Button>
          <Text fz="sm" c="dimmed">第 {pageIndex + 1} 页</Text>
          <Button variant="default" disabled={!hasNext || search.isPending} loading={search.isPending} onClick={next}>
            下一页
          </Button>
        </Group>
      ) : null}
    </Stack>
  )
}

function SearchResults({ items, failed }: { items?: Item[]; failed: number }) {
  return (
    <SectionCard title="结果" count={items ? `${items.length} 条` : undefined}>
      {!items ? (
        <EmptyState title="输入关键词开始搜索。" />
      ) : items.length === 0 ? (
        <EmptyState title="没有找到匹配内容。" hint={failed > 0 ? '部分来源未能完成搜索，请在活动中查看。' : undefined} />
      ) : (
        <>
          {failed > 0 && (
            <CardRow>
              <Text fz="sm" c="orange">部分来源未能完成搜索，已返回其余结果。</Text>
            </CardRow>
          )}
          {items.map((item) => <ResultRow key={item.id} item={item} />)}
        </>
      )}
    </SectionCard>
  )
}

function ResultRow({ item }: { item: Item }) {
  const excerpt = item.content?.text?.trim() || htmlText(item.content?.html)
  const sources = [...new Set(item.observations.map((observation) => observation.source))]
  return (
    <CardRow>
      <Stack gap={6}>
        <Anchor href={item.url} target="_blank" rel="noopener noreferrer" fw={600} fz="md">
          {item.title || item.url}
        </Anchor>
        {excerpt && <Text fz="sm" c="dimmed" lineClamp={3}>{excerpt}</Text>}
        <Group gap="xs" wrap="wrap">
          {sources.map((source) => <Badge key={source} variant="light">{source}</Badge>)}
          {item.published_at && <Text fz="xs" c="dimmed">{new Date(item.published_at).toLocaleString('zh-CN')}</Text>}
        </Group>
      </Stack>
    </CardRow>
  )
}

function htmlText(html?: string): string {
  if (!html) return ''
  return new DOMParser().parseFromString(html, 'text/html').body.textContent?.trim() ?? ''
}
