<script setup lang="ts">
import type { NavigationMenuItem } from '@nuxt/ui'

const route = useRoute()

const open = ref(false)

const links = [[{
  label: 'Overview',
  icon: 'i-lucide-house',
  to: '/',
  onSelect: () => {
    open.value = false
  }
}, {
  label: 'Compute',
  icon: 'i-lucide-server',
  defaultOpen: true,
  type: 'trigger',
  children: [{
    label: 'Instances',
    to: '/compute/instances',
    onSelect: () => {
      open.value = false
    }
  }]
}, {
  label: 'Network',
  icon: 'i-lucide-network',
  defaultOpen: true,
  type: 'trigger',
  children: [{
    label: 'Cloud networks',
    to: '/vpc/networks',
    onSelect: () => {
      open.value = false
    }
  }]
}, {
  label: 'Storage',
  icon: 'i-lucide-hard-drive',
  defaultOpen: true,
  type: 'trigger',
  children: [{
    label: 'Disks',
    to: '/disks',
    onSelect: () => {
      open.value = false
    }
  }]
}, {
  label: 'Operations',
  icon: 'i-lucide-list-checks',
  to: '/operations',
  badge: '2',
  onSelect: () => {
    open.value = false
  }
}, {
  label: 'Cluster',
  icon: 'i-lucide-boxes',
  to: '/cluster',
  onSelect: () => {
    open.value = false
  }
}, {
  label: 'Settings',
  to: '/settings',
  icon: 'i-lucide-settings',
  defaultOpen: false,
  type: 'trigger',
  children: [{
    label: 'General',
    to: '/settings',
    exact: true,
    onSelect: () => {
      open.value = false
    }
  }, {
    label: 'Access',
    to: '/settings/members',
    onSelect: () => {
      open.value = false
    }
  }, {
    label: 'Notifications',
    to: '/settings/notifications',
    onSelect: () => {
      open.value = false
    }
  }, {
    label: 'Security',
    to: '/settings/security',
    onSelect: () => {
      open.value = false
    }
  }]
}], [{
  label: 'Feedback',
  icon: 'i-lucide-message-circle',
  to: 'https://github.com/mindevis/qstack/issues',
  target: '_blank'
}, {
  label: 'Help & Support',
  icon: 'i-lucide-info',
  to: 'https://github.com/mindevis/qstack',
  target: '_blank'
}]] satisfies NavigationMenuItem[][]

const groups = computed(() => [{
  id: 'links',
  label: 'Go to',
  items: links.flat()
}, {
  id: 'code',
  label: 'Code',
  items: [{
    id: 'source',
    label: 'View page source',
    icon: 'i-simple-icons-github',
    to: `https://github.com/mindevis/qstack/blob/main/console/app/pages${route.path === '/' ? '/index' : route.path}.vue`,
    target: '_blank'
  }]
}])
</script>

<template>
  <UDashboardGroup unit="rem">
    <UDashboardSidebar
      id="default"
      v-model:open="open"
      collapsible
      resizable
      class="bg-elevated/25"
      :ui="{ footer: 'lg:border-t lg:border-default' }"
    >
      <template #header="{ collapsed }">
        <TeamsMenu :collapsed="collapsed" />
      </template>

      <template #default="{ collapsed }">
        <UDashboardSearchButton :collapsed="collapsed" class="bg-transparent ring-default" />

        <UNavigationMenu
          :collapsed="collapsed"
          :items="links[0]"
          orientation="vertical"
          tooltip
          popover
        />

        <UNavigationMenu
          :collapsed="collapsed"
          :items="links[1]"
          orientation="vertical"
          tooltip
          class="mt-auto"
        />
      </template>

      <template #footer="{ collapsed }">
        <UserMenu :collapsed="collapsed" />
      </template>
    </UDashboardSidebar>

    <UDashboardSearch :groups="groups" />

    <slot />

    <NotificationsSlideover />
  </UDashboardGroup>
</template>
