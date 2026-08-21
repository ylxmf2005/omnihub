import { Button, Stack, Text } from '@mantine/core'
import { Link } from 'react-router'
import { PageHeader } from '../components/layout'

export function NotFound() {
  return (
    <Stack gap="md">
      <PageHeader title="找不到这个页面" />
      <Text fz="sm" c="dimmed">
        地址可能输错了，或这个页面已经改名。
      </Text>
      <Button component={Link} to="/" variant="default" w="fit-content">
        回到概览
      </Button>
    </Stack>
  )
}
