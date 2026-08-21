/**
 * Catalog — read-only reference for what this instance can actually do.
 */

import { Box, Group, Stack, Table, Text } from '@mantine/core'
import { useRouteTemplates, useSources } from '../api/queries'
import type { RouteTemplate, Source } from '../api/types'
import { StateBadge } from '../components/display'
import { LIMITATION_COPY, describeProblem } from '../domain/vocabulary'
import {
  CardRow,
  Caveat,
  EmptyState,
  ErrorAlert,
  Fact,
  FieldList,
  JsonBlock,
  LoadingRow,
  MetaLine,
  PageHeader,
  SectionCard,
} from '../components/layout'

const ORIGIN_LABEL: Record<string, string> = { builtin: '内置', user: '用户添加' }

const CAPABILITY_LABEL: Record<string, string> = {
  search: '搜索',
  latest: '最新',
  fetch: '按 URL 获取',
}

const CONTENT_LEVEL_LABEL: Record<string, string> = {
  body: '正文',
  summary: '摘要',
  metadata: '仅元数据',
  snippet: '片段摘录',
}

const PAGINATION_LABEL: Record<string, string> = {
  none: '不分页',
  page: '按页分页',
}

const TIME_RANGE_LABEL: Record<string, string> = {
  feed_window: 'Feed 当前窗口',
  unsupported: '不支持按时间筛选',
  recent_window: '仅最近窗口',
}

const AUTH_KIND_LABEL: Record<string, string> = {
  none: '无',
  token: 'Token',
  api_key: 'API Key',
  app_only: 'App-only 认证',
}

const COST_LABEL: Record<string, string> = {
  free: '免费',
  rate_limited: '免费但受限流',
  metered: '按量计费',
  paid: '付费',
}

const TRUST_LABEL: Record<string, string> = {
  remote_public: '公开的远程来源',
  official_api: '官方 API',
  configured_endpoint: '已配置的 Endpoint',
  local_executable: '本地可执行程序',
}

function sourceLabel(source: Source): string {
  return source.display_name || source.id
}

/** Only these two adapters implement layered Probe (see PROBLEM_COPY.probe_not_configured). */
function probeCapable(template: RouteTemplate): boolean {
  return template.adapter === 'feed' || template.adapter === 'rsshub'
}

export function Catalog() {
  const sources = useSources()
  const templates = useRouteTemplates()

  return (
    <Stack gap="lg">
      <PageHeader
        title="Catalog"
        description="这个实例已注册的 Source，以及每个 RouteTemplate 实际能做什么、需要什么。"
      />

      {sources.error ? (
        <ErrorAlert error={sources.error} onRetry={() => void sources.refetch()} />
      ) : null}
      {templates.error ? (
        <ErrorAlert error={templates.error} onRetry={() => void templates.refetch()} />
      ) : null}

      <SectionCard
        title="Source"
        count={sources.data && sources.data.length > 0 ? `${sources.data.length} 个` : undefined}
      >
        {sources.isLoading ? (
          <LoadingRow />
        ) : (sources.data ?? []).length === 0 ? (
          <EmptyState title="还没有已注册的 Source。" />
        ) : (
          <Table.ScrollContainer minWidth={480}>
            <Table verticalSpacing="sm" horizontalSpacing="md">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Source</Table.Th>
                  <Table.Th>来源</Table.Th>
                  <Table.Th>状态</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {(sources.data ?? []).map((source) => (
                  <Table.Tr key={source.id}>
                    <Table.Td>
                      <Text fz="sm">{sourceLabel(source)}</Text>
                      <Text fz="xs" c="dimmed" ff="monospace">
                        {source.id}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Text fz="sm">{ORIGIN_LABEL[source.origin] ?? source.origin}</Text>
                    </Table.Td>
                    <Table.Td>
                      <StateBadge
                        label={source.enabled ? '已启用' : '已停用'}
                        tone={source.enabled ? 'ok' : 'idle'}
                        code={source.enabled ? 'enabled' : 'disabled'}
                        meaning={
                          source.enabled
                            ? '这个 Source 当前可以被 Channel 使用。'
                            : '这个 Source 当前已停用。'
                        }
                      />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </SectionCard>

      <SectionCard
        title="RouteTemplate"
        count={
          templates.data && templates.data.length > 0 ? `${templates.data.length} 个` : undefined
        }
      >
        {templates.isLoading ? (
          <LoadingRow />
        ) : (templates.data ?? []).length === 0 ? (
          <EmptyState title="还没有已声明的 RouteTemplate。" />
        ) : (
          <Stack gap={0}>
            {(templates.data ?? []).map((template) => (
              <RouteTemplateRow key={template.route_template_id} template={template} />
            ))}
          </Stack>
        )}
      </SectionCard>
    </Stack>
  )
}

function RouteTemplateRow({ template }: { template: RouteTemplate }) {
  const scope = template.source_constraint.values ?? []
  const isAnyRegistered = template.source_constraint.kind === 'any_registered'

  return (
    <CardRow>
      <Stack gap="sm">
        <Group justify="space-between" align="flex-start" wrap="wrap">
          <Box>
            <Text fz="sm" fw={600} ff="monospace">
              {template.route_template_id}
            </Text>
            <MetaLine gap="sm">
              <Fact label="Provider">{template.provider}</Fact>
              <Fact label="Adapter">{template.adapter}</Fact>
            </MetaLine>
          </Box>
          <Group gap={6} wrap="wrap">
            {template.capabilities.map((capability) => (
              <StateBadge
                key={capability}
                label={CAPABILITY_LABEL[capability] ?? capability}
                tone="idle"
                code={capability}
                meaning="这个 RouteTemplate 支持的 Operation 类型之一。"
              />
            ))}
          </Group>
        </Group>

        <Text fz="xs" c="dimmed">
          {isAnyRegistered ? '适用于所有已注册的 Source。' : `仅适用于：${scope.join('、')}`}
        </Text>

        <FieldList
          labelWidth={110}
          fields={[
            {
              label: '内容层级',
              value: CONTENT_LEVEL_LABEL[template.content_level] ?? template.content_level,
            },
            {
              label: '分页',
              value:
                (PAGINATION_LABEL[template.pagination.kind] ?? template.pagination.kind) +
                (template.pagination.globally_mergeable ? '（可跨线路合并）' : ''),
            },
            {
              label: '时间范围',
              value: TIME_RANGE_LABEL[template.time_range.kind] ?? template.time_range.kind,
            },
            {
              label: '认证',
              value: template.auth.required
                ? `需要 ${AUTH_KIND_LABEL[template.auth.kind] ?? template.auth.kind}`
                : template.auth.kind === 'none'
                  ? '不需要认证'
                  : `不需要认证（可选 ${AUTH_KIND_LABEL[template.auth.kind] ?? template.auth.kind}）`,
            },
            { label: '费用', value: COST_LABEL[template.cost] ?? template.cost },
            { label: '可信度', value: TRUST_LABEL[template.trust] ?? template.trust },
            ...(template.endpoint_required
              ? [{ label: 'Endpoint', value: '需要绑定 Endpoint 才能使用' }]
              : []),
          ]}
        />

        {template.limitations.length > 0 && (
          <Box>
            <Text fz="xs" c="dimmed" mb={4}>
              限制
            </Text>
            <Stack gap={4}>
              {template.limitations.map((code) => (
                <Text key={code} fz="xs">
                  {LIMITATION_COPY[code] ?? (
                    <Text span ff="monospace" fz={10} c="dimmed">
                      {code}
                    </Text>
                  )}
                </Text>
              ))}
            </Stack>
          </Box>
        )}

        {!probeCapable(template) && (
          <Caveat>{describeProblem('probe_not_configured').hint}</Caveat>
        )}

        {template.parameters_schema ? (
          <Box>
            <Text fz="xs" c="dimmed" mb={4}>
              parameters_schema
            </Text>
            <JsonBlock value={template.parameters_schema} maxHeight={220} />
          </Box>
        ) : (
          <Text fz="xs" c="dimmed">
            这个 RouteTemplate 不需要额外参数。
          </Text>
        )}
      </Stack>
    </CardRow>
  )
}
