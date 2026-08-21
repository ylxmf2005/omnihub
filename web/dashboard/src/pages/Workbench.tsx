/**
 * Workbench — run a query against the Query plane and read the Envelope back.
 *
 * Execution here is synchronous: `POST /v1/search|latest|fetch` returns the
 * Envelope in the response. It does not create a Run — the Backend's Run log
 * holds View refreshes and Channel Probes only — so this page never tells the
 * user to go look for one.
 *
 * Two things are derived from real registry data rather than assumed:
 *
 *   **Which Channels can serve the chosen Operation.** A Channel's RouteTemplate
 *   declares its `capabilities`, so the picker only offers Channels that actually
 *   support the selected Operation. Without this the user picks a plausible
 *   combination and gets `409 execution_conflict, no configured Channel can
 *   execute the request` — a true statement about routing, dressed up as a
 *   conflict. Better to not offer the impossible request.
 *
 *   **The exact body.** It is shown verbatim, because the body is the reusable
 *   artifact: three of its constraints (`schema_version` required, no `operation`
 *   field, `route_policy.mode` non-empty) are only discoverable by trying.
 *
 * A `partial` is a success with gaps, so it renders as a result. Only a refusal or
 * a transport failure reaches `ErrorAlert`.
 */

import { useMemo, useState } from 'react'
import {
  Button,
  Group,
  MultiSelect,
  NumberInput,
  Select,
  Stack,
  Switch,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPlayerPlay } from '@tabler/icons-react'
import { READ_ONLY, useChannels, useRouteTemplates, useRunQuery } from '../api/queries'
import type {
  FetchInput,
  LatestInput,
  OperationKind,
  RoutePolicy,
  RouteSelector,
  SearchInput,
} from '../api/types'
import { EnvelopeView, OPERATION_COPY } from '../components/EnvelopeView'
import { RunBadge } from '../components/display'
import {
  CardRow,
  Caveat,
  DemoModeNotice,
  EmptyState,
  ErrorAlert,
  JsonBlock,
  PageHeader,
  PageLink,
  SectionCard,
} from '../components/layout'

const OPERATION_DESCRIPTION: Record<OperationKind, string> = {
  latest: '按时间取一批最新内容，不需要关键词。',
  search: '在选中的线路里按关键词检索。',
  fetch: '取回一个具体地址的内容。',
}

/**
 * `exclude` is deliberately absent. The selector list is derived from the
 * Channels chosen above, so an exclude request would always exclude everything it
 * scoped and could only ever come back `409 execution_conflict`.
 */
const ROUTE_MODE_COPY: Record<string, { label: string; description: string }> = {
  auto: { label: '自动选路', description: '由 Backend 按优先级选择线路。' },
  prefer: { label: '优先所选', description: '优先使用选中的 Channel，必要时仍可用其他线路。' },
  only: { label: '只用所选', description: '只允许选中的 Channel 执行，不回退到其他线路。' },
}

const DEDUPE_COPY: Record<string, string> = {
  exact: '合并同一条内容的重复出现',
  none: '保留每一次出现',
}

export function Workbench() {
  const channels = useChannels()
  const templates = useRouteTemplates()

  const [operation, setOperation] = useState<OperationKind>('latest')
  const [selected, setSelected] = useState<string[]>([])
  const [query, setQuery] = useState('')
  const [target, setTarget] = useState('')
  const [limit, setLimit] = useState(20)
  const [dedupe, setDedupe] = useState<'none' | 'exact'>('exact')
  const [deadline, setDeadline] = useState(30000)
  const [mode, setMode] = useState('auto')
  const [aggregate, setAggregate] = useState(false)
  const [allowFallback, setAllowFallback] = useState(false)

  // One mutation per endpoint; hooks cannot be called conditionally.
  const search = useRunQuery('search')
  const latest = useRunQuery('latest')
  const fetchOne = useRunQuery('fetch')
  const active = operation === 'search' ? search : operation === 'fetch' ? fetchOne : latest

  // A Channel can serve an Operation only if its RouteTemplate declares it.
  const capabilityOf = useMemo(() => {
    const map = new Map<string, OperationKind[]>()
    for (const template of templates.data ?? []) {
      map.set(template.route_template_id, template.capabilities)
    }
    return map
  }, [templates.data])

  const eligible = (channels.data ?? []).filter(
    (channel) =>
      channel.enabled && (capabilityOf.get(channel.route_template_id) ?? []).includes(operation),
  )
  const eligibleIds = new Set(eligible.map((channel) => channel.id))
  const scope = selected.filter((id) => eligibleIds.has(id))

  const routePolicy: RoutePolicy = {
    mode: mode as RoutePolicy['mode'],
    aggregate,
    allow_fallback: allowFallback,
    // A named mode needs a non-empty selector list; `scope` is guaranteed
    // non-empty before execution is allowed.
    ...(mode === 'auto'
      ? {}
      : { [mode]: scope.map((id): RouteSelector => ({ kind: 'channel', id })) }),
  }

  // Three endpoints, three body shapes. Each is built to its own type so the
  // compiler — not a live 400 — catches a field the endpoint would reject.
  const latestBody: LatestInput = {
    schema_version: '1.0',
    scope: { channels: scope },
    route_policy: routePolicy,
    limit,
    time_range: {},
    identity_dedupe: dedupe,
    similarity_grouping: 'off',
    deadline_ms: deadline,
  }
  const searchBody: SearchInput = { ...latestBody, query: query.trim() }
  const fetchBody: FetchInput = {
    schema_version: '1.0',
    target: target.trim(),
    scope: { channels: scope },
    route_policy: routePolicy,
    deadline_ms: deadline,
  }

  const body: SearchInput | LatestInput | FetchInput =
    operation === 'fetch' ? fetchBody : operation === 'search' ? searchBody : latestBody

  const missing =
    scope.length === 0
      ? '至少选择一个 Channel。'
      : operation === 'search' && query.trim() === ''
        ? '搜索需要关键词。'
        : operation === 'fetch' && target.trim() === ''
          ? '抓取需要一个地址。'
          : undefined

  // `useRunQuery` is generic per kind, so each mutation only accepts its own body.
  const run = () => {
    if (operation === 'fetch') fetchOne.mutate(fetchBody)
    else if (operation === 'search') search.mutate(searchBody)
    else latest.mutate(latestBody)
  }

  // Each Operation has its own mutation, so all three are cleared — otherwise
  // switching back to an earlier Operation would show its stale result.
  const changeOperation = (next: OperationKind) => {
    setOperation(next)
    search.reset()
    latest.reset()
    fetchOne.reset()
  }

  return (
    <Stack gap="lg">
      <PageHeader
        title="Query Workbench"
        description="直接对 Query 平面执行一次取数，看到完整结果与覆盖范围。执行是同步的，不会留下 Run 记录。"
      />

      <DemoModeNotice />

      <SectionCard title="请求">
        <CardRow>
          <Stack gap="md">
            <Group grow align="flex-start">
              <Select
                label="Operation"
                description={OPERATION_DESCRIPTION[operation]}
                data={(['latest', 'search', 'fetch'] as OperationKind[]).map((value) => ({
                  value,
                  label: OPERATION_COPY[value] ?? value,
                }))}
                value={operation}
                onChange={(value) => changeOperation((value as OperationKind) ?? 'latest')}
                allowDeselect={false}
              />
              <Select
                label="选路方式"
                description={ROUTE_MODE_COPY[mode]?.description}
                data={Object.entries(ROUTE_MODE_COPY).map(([value, copy]) => ({
                  value,
                  label: copy.label,
                }))}
                value={mode}
                onChange={(value) => setMode(value ?? 'auto')}
                allowDeselect={false}
              />
            </Group>

            <MultiSelect
              label="Channel"
              description={
                templates.isLoading || channels.isLoading
                  ? '正在读取可用线路…'
                  : `只列出 RouteTemplate 声明支持 ${OPERATION_COPY[operation] ?? operation} 的启用中 Channel。`
              }
              placeholder={eligible.length === 0 ? '没有可用的 Channel' : '选择一个或多个 Channel'}
              data={eligible.map((channel) => ({
                value: channel.id,
                label: channel.display_name ?? channel.id,
              }))}
              value={scope}
              onChange={setSelected}
              disabled={eligible.length === 0}
              searchable
              clearable
            />

            {operation === 'search' && (
              <TextInput
                label="关键词"
                description="在选中线路的可搜索范围内匹配。"
                placeholder="svg"
                value={query}
                onChange={(event) => setQuery(event.currentTarget.value)}
                required
              />
            )}

            {operation === 'fetch' && (
              <TextInput
                label="地址"
                description="要抓取的具体页面地址，或 owner/repo 形式的标识。"
                placeholder="https://www.v2ex.com/t/1235976"
                value={target}
                onChange={(event) => setTarget(event.currentTarget.value)}
                required
              />
            )}

            <Group grow align="flex-start">
              {operation !== 'fetch' && (
                <NumberInput
                  label="条数上限"
                  description="1 到 100。"
                  value={limit}
                  onChange={(value) => setLimit(typeof value === 'number' ? value : 1)}
                  min={1}
                  max={100}
                  clampBehavior="strict"
                />
              )}
              <NumberInput
                label="超时"
                description="毫秒，最多 120000。超时会让结果不完整。"
                value={deadline}
                onChange={(value) => setDeadline(typeof value === 'number' ? value : 1)}
                min={1}
                max={120000}
                step={1000}
                clampBehavior="strict"
              />
              {operation !== 'fetch' && (
                <Select
                  label="重复内容"
                  description={DEDUPE_COPY[dedupe]}
                  data={Object.keys(DEDUPE_COPY).map((value) => ({
                    value,
                    label: DEDUPE_COPY[value],
                  }))}
                  value={dedupe}
                  onChange={(value) => setDedupe((value as 'none' | 'exact') ?? 'exact')}
                  allowDeselect={false}
                />
              )}
            </Group>

            <Group gap="lg" wrap="wrap">
              <Switch
                label="合并多条线路的结果"
                checked={aggregate}
                onChange={(event) => setAggregate(event.currentTarget.checked)}
              />
              <Switch
                label="允许使用备用线路"
                checked={allowFallback}
                onChange={(event) => setAllowFallback(event.currentTarget.checked)}
              />
            </Group>

            {eligible.length === 0 && !channels.isLoading && !templates.isLoading && (
              <Caveat>
                目前没有任何启用中的 Channel 支持{OPERATION_COPY[operation] ?? operation}
                。这取决于 Channel 绑定的 RouteTemplate 声明了哪些能力。
                <PageLink to="/catalog">查看 RouteTemplate 的能力</PageLink>
              </Caveat>
            )}

            <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
              <Text fz="xs" c="dimmed">
                {missing ?? '请求已经完整，可以执行。'}
              </Text>
              <Group gap="xs">
                {active.data && <RunBadge status={active.data.status} />}
                <Button
                  leftSection={<IconPlayerPlay size={14} />}
                  onClick={run}
                  loading={active.isPending}
                  disabled={READ_ONLY || Boolean(missing)}
                >
                  执行
                </Button>
              </Group>
            </Group>
          </Stack>
        </CardRow>

        <CardRow>
          <Text fz="xs" c="dimmed" mb={6}>
            将要发送到 /v1/{operation} 的请求体
          </Text>
          <JsonBlock value={body} maxHeight={260} />
        </CardRow>
      </SectionCard>

      {active.error ? (
        <ErrorAlert error={active.error} onRetry={() => run()} />
      ) : null}

      {active.data ? (
        <EnvelopeView envelope={active.data} />
      ) : (
        <SectionCard title="结果">
          <EmptyState
            title="还没有执行。"
            hint="执行后这里会显示完整的 Envelope：取到的 Items、每条线路的执行情况，以及覆盖了多少范围。"
          />
        </SectionCard>
      )}
    </Stack>
  )
}
