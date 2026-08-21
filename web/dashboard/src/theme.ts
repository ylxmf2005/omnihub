/**
 * Mantine theme.
 *
 * Carries over the confirmed visual direction: deep petrol accent, dense
 * operator-tool spacing, Chinese-first system sans with monospace reserved for
 * machine identity. System font stacks only — the Dashboard must load offline.
 */

import { createTheme, type MantineColorsTuple } from '@mantine/core'

const petrol: MantineColorsTuple = [
  '#eef6f9',
  '#dceaf0',
  '#b6d3de',
  '#8dbbcc',
  '#6aa7bd',
  '#539bb4',
  '#4494b1',
  '#33819c',
  '#22738c',
  '#00637c',
]

export const theme = createTheme({
  primaryColor: 'petrol',
  primaryShade: { light: 9, dark: 4 },
  colors: { petrol },

  fontFamily:
    '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif',
  fontFamilyMonospace:
    'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace',

  defaultRadius: 'sm',

  fontSizes: {
    xs: '11px',
    sm: '12px',
    md: '13px',
    lg: '14px',
    xl: '16px',
  },

  headings: {
    fontWeight: '600',
    sizes: {
      h1: { fontSize: '20px', lineHeight: '1.25' },
      h2: { fontSize: '16px', lineHeight: '1.3' },
      h3: { fontSize: '14px', lineHeight: '1.35' },
    },
  },

  lineHeights: { sm: '1.35', md: '1.45', lg: '1.6' },

  components: {
    Paper: { defaultProps: { withBorder: true, radius: 'md' } },
    Card: { defaultProps: { withBorder: true, radius: 'md', padding: 'md' } },
    Button: { defaultProps: { size: 'xs' } },
    Badge: { defaultProps: { radius: 'sm' } },
    Table: { defaultProps: { verticalSpacing: 'xs', horizontalSpacing: 'md' } },
    Tooltip: { defaultProps: { withArrow: true, openDelay: 250, fz: 'xs' } },
  },
})
