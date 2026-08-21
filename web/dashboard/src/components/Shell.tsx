/**
 * App shell: sidebar navigation, content column, and the instance identity that
 * answers "which process am I actually talking to".
 *
 * Navigation is driven by the router, so the active item is derived from the URL
 * rather than tracked separately — a second source of truth for "where am I"
 * goes stale the first time someone navigates by link or back button.
 */

import {
  AppShell,
  Box,
  Burger,
  Group,
  NavLink,
  ScrollArea,
  Stack,
  Text,
  Tooltip,
} from '@mantine/core'
import { useDisclosure } from '@mantine/hooks'
import {
  IconActivity,
  IconBook2,
  IconFolders,
  IconKey,
  IconLayersSubtract,
  IconRss,
  IconSearch,
  IconShare3,
  IconSitemap,
  IconSparkles,
  IconStethoscope,
  IconWorld,
} from '@tabler/icons-react'
import type { ReactNode } from 'react'
import { Link, useLocation } from 'react-router'
import { IdChip } from './display'
import { useSummary } from '../api/queries'

interface NavItem {
  label: string
  to: string
  icon: ReactNode
}

const NAV_GROUPS: { label?: string; items: NavItem[] }[] = [
  {
    items: [
      { label: '概览', to: '/', icon: <IconLayersSubtract size={16} /> },
      { label: 'Query Workbench', to: '/workbench', icon: <IconSearch size={16} /> },
    ],
  },
  {
    label: '接入',
    items: [
      { label: 'Channels', to: '/channels', icon: <IconRss size={16} /> },
      { label: 'Connections', to: '/connections', icon: <IconSitemap size={16} /> },
      { label: 'Credentials', to: '/credentials', icon: <IconKey size={16} /> },
      { label: 'Semantic Profiles', to: '/semantic-profiles', icon: <IconSparkles size={16} /> },
    ],
  },
  {
    label: '分发',
    items: [
      { label: 'Collections', to: '/collections', icon: <IconFolders size={16} /> },
      { label: 'Views', to: '/views', icon: <IconLayersSubtract size={16} /> },
    ],
  },
  {
    label: '运行',
    items: [
      { label: 'Runs', to: '/runs', icon: <IconActivity size={16} /> },
      { label: 'Diagnostics', to: '/diagnostics', icon: <IconStethoscope size={16} /> },
    ],
  },
  {
    label: '系统',
    items: [
      { label: 'Browser Bridge', to: '/bridge', icon: <IconWorld size={16} /> },
      { label: 'Catalog', to: '/catalog', icon: <IconBook2 size={16} /> },
    ],
  },
]

/** `/channels/abc` keeps Channels lit; only `/` must match exactly. */
function isActive(pathname: string, to: string): boolean {
  return to === '/' ? pathname === '/' : pathname === to || pathname.startsWith(`${to}/`)
}

export function Shell({ children }: { children: ReactNode }) {
  const [navOpen, { toggle: toggleNav, close: closeNav }] = useDisclosure(false)
  const { pathname } = useLocation()
  const summary = useSummary()

  return (
    <AppShell
      // Below `sm` the navbar becomes an overlay drawer. Left expanded on mobile
      // it filled the whole 390px viewport and pushed the page off-screen.
      navbar={{ width: 216, breakpoint: 'sm', collapsed: { mobile: !navOpen } }}
      header={{ height: 48, collapsed: false }}
      padding={0}
    >
      {/* Exists to carry the mobile nav trigger, so it is hidden once the sidebar
          is permanently visible. */}
      <AppShell.Header hiddenFrom="sm">
        <Group h="100%" px="sm" gap="sm">
          <Burger opened={navOpen} onClick={toggleNav} size="sm" aria-label="导航" />
          <Group gap={6}>
            <Mark size={22} icon={13} />
            <Text fw={600} fz="md">
              OmniHub
            </Text>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar>
        <AppShell.Section p="sm" visibleFrom="sm">
          <Group gap={8} wrap="nowrap">
            <Mark size={24} icon={14} />
            <Text fw={600} fz="lg">
              OmniHub
            </Text>
          </Group>
        </AppShell.Section>

        <AppShell.Section grow component={ScrollArea} px="xs" pt={{ base: 'sm', sm: 0 }}>
          <Stack gap="md" pb="sm">
            {NAV_GROUPS.map((group, index) => (
              <Box key={group.label ?? index}>
                {group.label && (
                  <Text fz={10} fw={600} c="dimmed" px="xs" pb={4} tt="uppercase" lts="0.04em">
                    {group.label}
                  </Text>
                )}
                {group.items.map((item) => (
                  <NavLink
                    key={item.to}
                    component={Link}
                    to={item.to}
                    label={item.label}
                    leftSection={item.icon}
                    active={isActive(pathname, item.to)}
                    fz="md"
                    py={6}
                    onClick={closeNav}
                  />
                ))}
              </Box>
            ))}
          </Stack>
        </AppShell.Section>

        {/* Instance identity is "which process am I talking to", so it stays
            visible on every page instead of being a homepage banner. */}
        {summary.data && (
          <AppShell.Section p="sm" style={{ borderTop: '1px solid var(--app-shell-border-color)' }}>
            <Stack gap={2}>
              <IdentityRow label="监听">
                <Text fz={10} ff="monospace" c="dimmed">
                  127.0.0.1:8787
                </Text>
              </IdentityRow>
              <IdentityRow label="版本">
                <Tooltip label={summary.data.version} fz="xs">
                  <Text
                    fz={10}
                    ff="monospace"
                    c="dimmed"
                    truncate
                    style={{ maxWidth: 118, cursor: 'help' }}
                  >
                    {summary.data.version}
                  </Text>
                </Tooltip>
              </IdentityRow>
              <IdentityRow label="Instance">
                <IdChip value={summary.data.instance_id} width={118} />
              </IdentityRow>
            </Stack>
          </AppShell.Section>
        )}
      </AppShell.Navbar>

      <AppShell.Main>
        {/*
          Capped and left-aligned. A fluid column read correctly at 1280px but at
          2000px it stretched three stat cards to ~500px each around a two-digit
          number; centring the cap instead detached content from its navigation.
        */}
        <Box p={{ base: 'md', sm: 'lg' }} style={{ maxWidth: 1280 }}>
          {children}
        </Box>
      </AppShell.Main>
    </AppShell>
  )
}

function IdentityRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Group justify="space-between" gap="xs" wrap="nowrap">
      <Text fz={10} c="dimmed">
        {label}
      </Text>
      {children}
    </Group>
  )
}

function Mark({ size, icon }: { size: number; icon: number }) {
  return (
    <Box
      style={{
        display: 'grid',
        placeItems: 'center',
        width: size,
        height: size,
        borderRadius: 4,
        background: 'var(--mantine-color-petrol-9)',
        color: 'white',
        flex: 'none',
      }}
    >
      <IconShare3 size={icon} stroke={2.2} />
    </Box>
  )
}
