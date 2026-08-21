/**
 * Browser Bridge — connection status, granted origins, and honest setup notes.
 *
 * This page's job is accuracy, not encouragement. The chain it describes
 * (Companion Extension -> Native Messaging host -> Bridge IPC) has its Backend
 * half implemented and reachable here; the extension half is not installed in
 * this environment and is not part of what ships. Nothing here may imply
 * otherwise, and `connected: false` / a zero `last_seen_at` must read as "never
 * connected", not as a stale timestamp.
 */

import { useState } from 'react'
import { Box, Button, Code, Group, Stack, Text, Tooltip } from '@mantine/core'
import {
  IconPlugConnected,
  IconPlugConnectedX,
  IconTrash,
  IconWorld,
} from '@tabler/icons-react'
import { READ_ONLY, useBridge, useRevokeOrigin } from '../api/queries'
import type { BrowserBridge, OmniError } from '../api/types'
import { StateBadge, Timestamp } from '../components/display'
import { describeError } from '../domain/vocabulary'
import {
  CardRow,
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

/** Go's zero time. `last_seen_at` carries this when the Bridge has never connected. */
function neverConnected(iso: string): boolean {
  return !iso || new Date(iso).getUTCFullYear() <= 1
}

export function BrowserBridgePage() {
  const bridge = useBridge()
  const data = bridge.data

  return (
    <Stack gap="lg">
      <PageHeader
        title="Browser Bridge"
        description="有些 Source 只在登录后才能读取。对这些 Source，OmniHub 在执行的那一刻从本机 Chrome 借用 Cookie，用完即弃：Cookie 只存在于单次执行的内存里，不写入数据库，不经过 Dashboard 的 HTTP 接口，也不会出现在 Run、错误或日志里。每个站点的授权由你在扩展里对精确的 HTTPS Origin 做一次手动操作完成，授权关系由 Chrome 保管，OmniHub 只是使用者。"
      />

      <DemoModeNotice />

      {bridge.error ? (
        <ErrorAlert error={bridge.error} onRetry={() => void bridge.refetch()} />
      ) : null}

      <SectionCard title="连接状态">
        {bridge.isLoading ? <LoadingRow /> : data ? <ConnectionStatus bridge={data} /> : null}
      </SectionCard>

      {data ? <GrantedOrigins bridge={data} /> : null}

      <SectionCard title="如何启用">
        <Box px="md" pb="md" pt={4}>
          <Text fz="sm">
            这条链路由三部分组成：一个 Chrome MV3 Companion Extension、一个 Native Messaging host
            （<Code>omnihub chrome-host</Code>），以及 Bridge 自身的 IPC。Backend 侧（host、IPC、授权协议、就绪检查）已经实现；扩展本身还没有作为产品的一部分发布，当前环境也没有安装它。
          </Text>
          <Text fz="sm" mt="sm">
            仓库里已经有一份扩展源码，位于 <Code>extension/</Code> 目录。要启用它，需要在 Chrome 里以“加载已解压的扩展程序”方式加载这个目录，取得扩展 ID 后执行：
          </Text>
          <Code block mt={6} fz="xs">
            omnihub chrome-host install --extension-id {'<ID>'}
          </Code>
          <Text fz="xs" c="dimmed" mt="sm">
            完成这两步之前，依赖 Cookie 的 Channel 都无法执行。
          </Text>
        </Box>
      </SectionCard>

      <SectionCard title="相关 Channel">
        <Box px="md" pb="md" pt={4}>
          <Text fz="sm">需要登录态才能执行的 Channel，会在 Channels 页面显示“待登录”或“待授权”。</Text>
          <Box mt={6}>
            <PageLink to="/channels">查看 Channels</PageLink>
          </Box>
        </Box>
      </SectionCard>
    </Stack>
  )
}

function ConnectionStatus({ bridge }: { bridge: BrowserBridge }) {
  const neverSeen = neverConnected(bridge.last_seen_at)

  return (
    <Box px="md" pb="md" pt={4}>
      <Group gap="sm" wrap="wrap">
        <StateBadge
          label={bridge.connected ? '已连接' : '未连接'}
          tone={bridge.connected ? 'ok' : 'bad'}
          code={bridge.connected ? 'connected' : 'disconnected'}
          meaning={
            bridge.connected
              ? 'Bridge 当前在线，依赖 Cookie 的 Channel 可以借用登录态执行。'
              : 'Bridge 当前不在线，依赖 Cookie 的 Channel 现在无法执行。'
          }
          icon={
            bridge.connected ? (
              <IconPlugConnected size={12} stroke={2.4} />
            ) : (
              <IconPlugConnectedX size={12} stroke={2.4} />
            )
          }
        />
        <Text fz="xs" c="dimmed" ff="monospace">
          {bridge.browser}
        </Text>
      </Group>

      <MetaLine gap="lg">
        <Fact label="Profile">{bridge.profile_label || '未提供'}</Fact>
        <Fact label="最近一次连接">
          {neverSeen ? (
            <Text span fz="xs" c="dimmed">
              从未连接
            </Text>
          ) : (
            <Timestamp iso={bridge.last_seen_at} relative />
          )}
        </Fact>
      </MetaLine>

      {bridge.last_error ? (
        <Box mt="sm">
          <BridgeErrorNote error={bridge.last_error} />
        </Box>
      ) : null}
    </Box>
  )
}

/**
 * `browser_unavailable` (Bridge itself offline) and `browser_permission_missing`
 * (Bridge online, one origin not granted) need different fixes, so they must
 * never collapse into one message. `describeError` already carries distinct
 * copy per code — this just renders whichever one the Backend actually sent.
 */
function BridgeErrorNote({ error }: { error: OmniError }) {
  const copy = describeError(error.code, error.retryable)
  return (
    <Box>
      <Text fz="sm">{copy.text}</Text>
      {copy.hint && (
        <Text fz="xs" c="dimmed" mt={2}>
          {copy.hint}
        </Text>
      )}
      <Text fz={10} c="dimmed" mt={4} ff="monospace">
        {error.code}
      </Text>
    </Box>
  )
}

function GrantedOrigins({ bridge }: { bridge: BrowserBridge }) {
  const origins = bridge.granted_origins ?? []

  return (
    <SectionCard title="已授权站点" count={origins.length > 0 ? `${origins.length} 个` : undefined}>
      {origins.length === 0 ? (
        <EmptyState
          title="还没有任何站点被授权。"
          hint="授权在扩展里按精确的 HTTPS Origin 完成一次；这里只能查看和撤销，不能新增。"
        />
      ) : (
        <Stack gap={0}>
          {origins.map((pattern) => (
            <OriginRow key={pattern} bridgeId={bridge.id} pattern={pattern} connected={bridge.connected} />
          ))}
        </Stack>
      )}
    </SectionCard>
  )
}

/**
 * Revocation is forwarded to the extension over the Bridge IPC, so it requires
 * the Bridge to be online. Offline, the action is disabled with the reason
 * shown rather than attempted and silently failing as if it had succeeded.
 */
function OriginRow({
  bridgeId,
  pattern,
  connected,
}: {
  bridgeId: string
  pattern: string
  connected: boolean
}) {
  const revoke = useRevokeOrigin()
  const [confirming, setConfirming] = useState(false)

  const disabledReason = READ_ONLY
    ? '写操作已禁用（演示模式）。'
    : !connected
      ? 'Bridge 未连接，撤销请求无法转发给扩展。'
      : undefined

  return (
    <CardRow>
      <Group justify="space-between" wrap="nowrap" align="center">
        <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
          <IconWorld size={14} style={{ flex: 'none', opacity: 0.6 }} />
          <Text fz="sm" ff="monospace" truncate>
            {pattern}
          </Text>
        </Group>
        <Tooltip label={disabledReason} disabled={!disabledReason}>
          <Box>
            <Button
              variant="subtle"
              color="red"
              size="compact-sm"
              leftSection={<IconTrash size={13} />}
              disabled={Boolean(disabledReason)}
              onClick={() => setConfirming(true)}
            >
              撤销
            </Button>
          </Box>
        </Tooltip>
      </Group>

      <ConfirmDialog
        opened={confirming}
        onClose={() => {
          setConfirming(false)
          revoke.reset()
        }}
        onConfirm={() =>
          revoke.mutate(
            { bridgeId, pattern },
            { onSuccess: () => setConfirming(false) },
          )
        }
        title="撤销 Origin 授权"
        target={pattern}
        consequence="撤销会转发给扩展并移除 Chrome 里的这条授权记录。依赖它的 Channel 之后需要重新登录或重新授权。"
        confirmLabel="撤销"
        loading={revoke.isPending}
        error={revoke.error}
      />
    </CardRow>
  )
}
