/** 主导航由 URL 派生激活状态，避免为当前位置维护第二份状态。 */

import {
  AppShell,
  Box,
  Burger,
  Group,
  NavLink,
  ScrollArea,
  Stack,
  Text,
} from '@mantine/core'
import { useDisclosure } from '@mantine/hooks'
import {
  IconActivity,
  IconRss,
  IconSearch,
  IconShare3,
  IconLayersSubtract,
} from '@tabler/icons-react'
import type { ReactNode } from 'react'
import { Link, useLocation } from 'react-router'

interface NavItem {
  label: string
  to: string
  icon: ReactNode
}

const NAV_ITEMS: NavItem[] = [
  { label: '搜索', to: '/', icon: <IconSearch size={16} /> },
  { label: '来源', to: '/sources', icon: <IconRss size={16} /> },
  { label: '订阅', to: '/subscriptions', icon: <IconLayersSubtract size={16} /> },
  { label: '活动', to: '/activity', icon: <IconActivity size={16} /> },
]

/** 根路径只匹配搜索页，其他入口同时覆盖自己的对象详情。 */
function isActive(pathname: string, to: string): boolean {
  return to === '/' ? pathname === '/' : pathname === to || pathname.startsWith(`${to}/`)
}

export function Shell({ children }: { children: ReactNode }) {
  const [navOpen, { toggle: toggleNav, close: closeNav }] = useDisclosure(false)
  const { pathname } = useLocation()

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
          <Stack gap={4} pb="sm">
            {NAV_ITEMS.map((item) => (
              <NavLink
                key={item.to}
                component={Link}
                to={item.to}
                label={item.label}
                leftSection={item.icon}
                active={isActive(pathname, item.to)}
                fz="md"
                py={8}
                onClick={closeNav}
              />
            ))}
          </Stack>
        </AppShell.Section>
      </AppShell.Navbar>

      <AppShell.Main>
        <Box p={{ base: 'md', sm: 'lg' }} style={{ width: '100%' }}>
          {children}
        </Box>
      </AppShell.Main>
    </AppShell>
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
