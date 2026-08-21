/**
 * Page-level building blocks.
 *
 * Two conventions are enforced here rather than left to each page:
 *
 *   1. **No glyph separators.** Related facts are separated by layout — spacing,
 *      dividers, dimmed labels — not by `·`, `|` or `→`. A middle dot makes the
 *      reader parse punctuation to find the field boundary, and an arrow pretends
 *      to be an icon while carrying no meaning a link does not already have.
 *      Use `MetaLine` for inline facts and `FieldList` for label/value pairs.
 *   2. **Failures are explained, not echoed.** `ErrorAlert` translates the two
 *      real error shapes (a Problem document, and an Envelope that arrived with a
 *      failing status) into what happened and what to do. The machine code stays
 *      available but never leads.
 */

import {
  Alert,
  Anchor,
  Box,
  Button,
  Code,
  Divider,
  Group,
  Modal,
  Paper,
  ScrollArea,
  Stack,
  Text,
  Title,
} from '@mantine/core'
import {
  IconAlertTriangle,
  IconInfoCircle,
  IconLock,
} from '@tabler/icons-react'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { EnvelopeError, ProblemError } from '../api/client'
import { READ_ONLY } from '../api/queries'
import { describeError, describeProblem } from '../domain/vocabulary'
import type { Envelope } from '../api/types'

// ---------------------------------------------------------------------------
// Page frame
// ---------------------------------------------------------------------------

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string
  description?: ReactNode
  actions?: ReactNode
}) {
  return (
    <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
      <Box style={{ minWidth: 0 }}>
        <Title order={1}>{title}</Title>
        {description && (
          <Text fz="sm" c="dimmed" mt={4}>
            {description}
          </Text>
        )}
      </Box>
      {actions && (
        <Group gap="xs" wrap="wrap">
          {actions}
        </Group>
      )}
    </Group>
  )
}

/**
 * A titled panel whose rows are separated by hairlines. `action` sits opposite
 * the title as a plain link — a text link is already an affordance, so it needs
 * no trailing arrow.
 */
export function SectionCard({
  title,
  action,
  children,
  count,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
  count?: ReactNode
}) {
  return (
    <Paper p={0}>
      <Group justify="space-between" p="md" pb="sm" wrap="wrap" gap="xs">
        <Group gap="sm" wrap="nowrap">
          <Title order={2}>{title}</Title>
          {count !== undefined && (
            <Text fz="xs" c="dimmed">
              {count}
            </Text>
          )}
        </Group>
        {action}
      </Group>
      {children}
    </Paper>
  )
}

/** A hairline-separated row inside a SectionCard. */
export function CardRow({ children, ...rest }: { children: ReactNode } & Record<string, unknown>) {
  return (
    <Box
      px="md"
      py="sm"
      style={{ borderTop: '1px solid var(--mantine-color-default-border)' }}
      {...rest}
    >
      {children}
    </Box>
  )
}

/** Link to another page. Text only: the underline and colour carry the affordance. */
export function PageLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Anchor component={Link} to={to} fz="xs">
      {children}
    </Anchor>
  )
}

// ---------------------------------------------------------------------------
// Facts without glyph separators
// ---------------------------------------------------------------------------

/**
 * Inline secondary facts. Each fact is its own node and separation is spacing,
 * which is what replaced the `A · B · C` strings this file's header warns about.
 */
export function MetaLine({ children, gap = 'md' }: { children: ReactNode; gap?: string }) {
  return (
    <Group gap={gap} wrap="wrap" style={{ rowGap: 4 }}>
      {children}
    </Group>
  )
}

/** One labelled fact. The dimmed label is the boundary marker. */
export function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Group gap={6} wrap="nowrap">
      <Text fz="xs" c="dimmed">
        {label}
      </Text>
      <Text fz="xs" component="span">
        {children}
      </Text>
    </Group>
  )
}

/** Vertical label/value pairs for detail surfaces. */
export function FieldList({
  fields,
  labelWidth = 140,
}: {
  fields: { label: string; value: ReactNode; hint?: string }[]
  labelWidth?: number
}) {
  return (
    <Stack gap={0}>
      {fields.map((field, index) => (
        <Group
          key={field.label}
          align="flex-start"
          wrap="nowrap"
          gap="md"
          py={8}
          style={
            index === 0
              ? undefined
              : { borderTop: '1px solid var(--mantine-color-default-border)' }
          }
        >
          <Box style={{ width: labelWidth, flex: 'none' }}>
            <Text fz="xs" c="dimmed">
              {field.label}
            </Text>
            {field.hint && (
              <Text fz={10} c="dimmed" mt={2}>
                {field.hint}
              </Text>
            )}
          </Box>
          <Box style={{ minWidth: 0, flex: '1 1 auto' }}>
            {typeof field.value === 'string' ? (
              <Text fz="sm">{field.value}</Text>
            ) : (
              field.value
            )}
          </Box>
        </Group>
      ))}
    </Stack>
  )
}

// ---------------------------------------------------------------------------
// States
// ---------------------------------------------------------------------------

/**
 * Empty state. Says what the thing is for, because an empty list on a first run
 * is the moment a user most needs to know why the page exists.
 */
export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string
  hint?: string
  action?: ReactNode
}) {
  return (
    <Box px="md" pb="md" pt={4}>
      <Text fz="sm">{title}</Text>
      {hint && (
        <Text fz="xs" c="dimmed" mt={4}>
          {hint}
        </Text>
      )}
      {action && <Box mt="sm">{action}</Box>}
    </Box>
  )
}

export function LoadingRow({ label = '正在读取…' }: { label?: string }) {
  return (
    <Box px="md" pb="md">
      <Text fz="sm" c="dimmed">
        {label}
      </Text>
    </Box>
  )
}

/**
 * The single place that turns a thrown error into something a person can act on.
 *
 * Three shapes arrive here:
 *   - `ProblemError`: the Backend refused before executing. Say what to change.
 *   - `EnvelopeError`: execution failed but the Envelope explains itself, so show
 *     its own errors rather than a generic message.
 *   - anything else: a transport failure.
 */
export function ErrorAlert({
  error,
  title,
  onRetry,
}: {
  error: unknown
  title?: string
  onRetry?: () => void
}) {
  if (!error) return null

  const retry = onRetry && (
    <Button variant="default" size="compact-sm" onClick={onRetry} mt="xs">
      重试
    </Button>
  )

  if (error instanceof ProblemError) {
    const copy = describeProblem(error.problem.code)
    return (
      <Alert
        variant="light"
        color={error.isRevisionConflict ? 'yellow' : 'red'}
        icon={<IconAlertTriangle size={16} />}
        title={title ?? copy.text}
      >
        {copy.hint && <Text fz="sm">{copy.hint}</Text>}
        {error.problem.detail && (
          <Text fz="xs" c="dimmed" mt={6}>
            Backend 说明：{error.problem.detail}
          </Text>
        )}
        <Text fz={10} c="dimmed" mt={4} ff="monospace">
          {error.problem.status} {error.problem.code}
        </Text>
        {retry}
      </Alert>
    )
  }

  if (error instanceof EnvelopeError) {
    const envelope = error.envelope as Partial<Envelope>
    const first = envelope.errors?.[0]
    const copy = first ? describeError(first.code, first.retryable) : undefined
    return (
      <Alert
        variant="light"
        color="red"
        icon={<IconAlertTriangle size={16} />}
        title={title ?? copy?.text ?? '这次执行没有成功'}
      >
        {copy?.hint && <Text fz="sm">{copy.hint}</Text>}
        {envelope.errors && envelope.errors.length > 1 && (
          <Text fz="xs" c="dimmed" mt={6}>
            共 {envelope.errors.length} 条线路错误，详情见 Run 记录。
          </Text>
        )}
        {retry}
      </Alert>
    )
  }

  return (
    <Alert
      variant="light"
      color="red"
      icon={<IconAlertTriangle size={16} />}
      title={title ?? '没有连上 Backend'}
    >
      <Text fz="sm">
        确认 <Code>omnihub serve</Code> 正在运行，并且开发代理指向它的 loopback 地址。
      </Text>
      {retry}
    </Alert>
  )
}

/** Shown on every page that can write, when writes cannot actually reach anything. */
export function DemoModeNotice() {
  if (!READ_ONLY) return null
  return (
    <Alert variant="light" color="gray" icon={<IconLock size={16} />} title="演示模式：只读">
      <Text fz="sm">
        当前读取的是随仓库保存的真实响应快照，写操作不会发出。要修改配置，请启动 Backend 并以{' '}
        <Code>VITE_OMNIHUB_LIVE=1</Code> 运行。
      </Text>
    </Alert>
  )
}

/** A caveat that is true but not a failure — coverage gaps, unsupported probes. */
export function Caveat({ children }: { children: ReactNode }) {
  return (
    <Alert variant="light" color="gray" icon={<IconInfoCircle size={16} />} p="sm">
      <Text fz="xs">{children}</Text>
    </Alert>
  )
}

// ---------------------------------------------------------------------------
// Destructive confirmation
// ---------------------------------------------------------------------------

/**
 * Confirms an irreversible action by naming the exact target. The Backend
 * refuses to delete a referenced resource (`resource_in_use`) rather than
 * cascading, so this dialog never promises to clean up dependents.
 */
export function ConfirmDialog({
  opened,
  onClose,
  onConfirm,
  title,
  target,
  consequence,
  confirmLabel = '删除',
  loading,
  error,
}: {
  opened: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  target: string
  consequence?: string
  confirmLabel?: string
  loading?: boolean
  error?: unknown
}) {
  return (
    <Modal opened={opened} onClose={onClose} title={title} centered size="md">
      <Stack gap="sm">
        <Text fz="sm">
          将要操作：
          <Text span ff="monospace" fz="sm" ml={6}>
            {target}
          </Text>
        </Text>
        {consequence && (
          <Text fz="sm" c="dimmed">
            {consequence}
          </Text>
        )}
        {error ? <ErrorAlert error={error} /> : null}
        <Group justify="flex-end" gap="xs" mt="xs">
          <Button variant="default" onClick={onClose}>
            取消
          </Button>
          <Button color="red" onClick={onConfirm} loading={loading}>
            {confirmLabel}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Raw payloads
// ---------------------------------------------------------------------------

/**
 * Verbatim JSON, for the places where the exact request or schema *is* the
 * content a user needs — a saved Operation, a RouteTemplate's parameter schema.
 * Scrolls rather than growing, so a long payload cannot push the page away.
 */
export function JsonBlock({ value, maxHeight = 320 }: { value: unknown; maxHeight?: number }) {
  return (
    <ScrollArea.Autosize mah={maxHeight} type="auto">
      <Code block fz={11} style={{ lineHeight: 1.55 }}>
        {JSON.stringify(value, null, 2)}
      </Code>
    </ScrollArea.Autosize>
  )
}

export { Divider }
