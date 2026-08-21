/**
 * Shared display primitives. Each one exists because the same judgment recurs
 * across pages; none of them wrap a Mantine component for its own sake.
 */

import { Badge, CopyButton, Text, Tooltip, UnstyledButton } from '@mantine/core'
import {
  IconAlertTriangle,
  IconBan,
  IconCheck,
  IconCircleCheck,
  IconCircleDashed,
  IconClock,
  IconCopy,
  IconGitBranch,
  IconHelpCircle,
  IconKey,
  IconLogin2,
  IconRefresh,
  IconX,
} from '@tabler/icons-react'
import type { ReactNode } from 'react'
import type { ReadinessState, RunStatus, ViewStatus } from '../api/types'
import { READINESS_COPY, RUN_COPY, VIEW_COPY, type Tone } from '../domain/vocabulary'

const TONE_COLOR: Record<Tone, string> = {
  ok: 'teal',
  conditional: 'cyan',
  warn: 'yellow',
  action: 'violet',
  bad: 'red',
  idle: 'gray',
}

const TONE_ICON: Record<Tone, ReactNode> = {
  ok: <IconCircleCheck size={12} stroke={2.4} />,
  conditional: <IconGitBranch size={12} stroke={2.4} />,
  warn: <IconAlertTriangle size={12} stroke={2.4} />,
  action: <IconKey size={12} stroke={2.4} />,
  bad: <IconX size={12} stroke={2.8} />,
  idle: <IconCircleDashed size={12} stroke={2.4} />,
}

/**
 * State chip. Icon plus words, never colour alone. The raw enum value lives in
 * the tooltip so the UI stays traceable to the API without shouting codes at
 * the reader.
 */
export function StateBadge({
  label,
  tone,
  code,
  meaning,
  icon,
}: {
  label: string
  tone: Tone
  code: string
  meaning: string
  icon?: ReactNode
}) {
  return (
    <Tooltip
      label={
        <>
          {meaning}
          <Text span fz={10} ff="monospace" c="dimmed" ml={6}>
            {code}
          </Text>
        </>
      }
      multiline
      w={260}
    >
      <Badge
        variant="light"
        color={TONE_COLOR[tone]}
        leftSection={icon ?? TONE_ICON[tone]}
        styles={{ label: { textTransform: 'none', fontWeight: 500 } }}
      >
        {label}
      </Badge>
    </Tooltip>
  )
}

export function ReadinessBadge({ state }: { state: ReadinessState }) {
  const copy = READINESS_COPY[state]
  const icon =
    state === 'needs_login' ? <IconLogin2 size={12} stroke={2.4} />
    : state === 'blocked' ? <IconBan size={12} stroke={2.4} />
    : state === 'unknown' ? <IconHelpCircle size={12} stroke={2.4} />
    : undefined
  return <StateBadge label={copy.label} tone={copy.tone} code={state} meaning={copy.meaning} icon={icon} />
}

export function ViewBadge({ status }: { status: ViewStatus }) {
  const copy = VIEW_COPY[status]
  const icon = status === 'refreshing' ? <IconRefresh size={12} stroke={2.4} /> : undefined
  return <StateBadge label={copy.label} tone={copy.tone} code={status} meaning={copy.meaning} icon={icon} />
}

export function RunBadge({ status }: { status: RunStatus }) {
  const copy = RUN_COPY[status]
  const icon =
    status === 'running' ? <IconRefresh size={12} stroke={2.4} />
    : status === 'queued' ? <IconClock size={12} stroke={2.4} />
    : status === 'complete' ? <IconCheck size={12} stroke={2.8} />
    : undefined
  return <StateBadge label={copy.label} tone={copy.tone} code={status} meaning={copy.meaning} icon={icon} />
}

/**
 * Local time is what a user reads; the exact ISO instant stays in the tooltip
 * because Run and Probe timing is evidence.
 */
export function Timestamp({ iso, relative = false }: { iso: string; relative?: boolean }) {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return <Text span c="dimmed">—</Text>

  const text = relative ? formatRelative(date) : date.toLocaleTimeString('zh-CN', { hour12: false })
  return (
    <Tooltip label={iso} fz="xs">
      <Text span ff="monospace" fz="xs" c="dimmed" style={{ cursor: 'help' }}>
        {text}
      </Text>
    </Tooltip>
  )
}

function formatRelative(date: Date): string {
  const seconds = Math.round((Date.now() - date.getTime()) / 1000)
  if (seconds < 60) return '刚刚'
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
  return date.toLocaleDateString('zh-CN')
}

/**
 * Machine identifier. Truncates from the tail because IDs are recognised by
 * their prefix, and reports a real state after the copy action.
 */
export function IdChip({ value, width = 150 }: { value: string; width?: number }) {
  return (
    <Text span ff="monospace" fz="xs" c="dimmed" style={{ display: 'inline-flex', alignItems: 'center', gap: 4, maxWidth: width }}>
      <Text span ff="monospace" fz="xs" truncate>
        {value}
      </Text>
      <CopyButton value={value} timeout={1400}>
        {({ copied, copy }) => (
          <Tooltip label={copied ? '已复制' : '复制'} fz="xs">
            <UnstyledButton
              onClick={copy}
              aria-label={copied ? '已复制' : `复制 ${value}`}
              style={{ display: 'grid', placeItems: 'center', width: 18, height: 18, flex: 'none' }}
            >
              {copied ? (
                <IconCheck size={12} stroke={2.6} color="var(--mantine-color-teal-6)" />
              ) : (
                <IconCopy size={12} stroke={2} />
              )}
            </UnstyledButton>
          </Tooltip>
        )}
      </CopyButton>
    </Text>
  )
}
