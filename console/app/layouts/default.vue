<script setup lang="ts">
import type { NavigationMenuItem } from '@nuxt/ui'

const route = useRoute()

const { t } = useI18n()

const open = ref(false)

const links = computed(() => [[{
  label: t('nav.overview'),
  icon: 'i-lucide-house',
  to: '/',
  onSelect: () => {
    open.value = false
  }
}, {
  label: t('nav.compute'),
  icon: 'i-lucide-server',
  defaultOpen: true,
  type: 'trigger',
  children: [{
    label: t('nav.instances'),
    to: '/compute/instances',
    onSelect: () => {
      open.value = false
    }
  }]
}, {
  label: t('nav.network'),
  icon: 'i-lucide-network',
  defaultOpen: true,
  type: 'trigger',
  children: [{
    label: t('nav.networks'),
    to: '/vpc/networks',
    onSelect: () => {
      open.value = false
    }
  }]
}, {
  label: t('nav.storage'),
  icon: 'i-lucide-hard-drive',
  defaultOpen: true,
  type: 'trigger',
  children: [{
    label: t('nav.disks'),
    to: '/disks',
    onSelect: () => {
      open.value = false
    }
  }]
}, {
  label: t('nav.operations'),
  icon: 'i-lucide-list-checks',
  to: '/operations',
  badge: '2',
  onSelect: () => {
    open.value = false
  }
}, {
  label: t('nav.cluster'),
  icon: 'i-lucide-boxes',
  to: '/cluster',
  onSelect: () => {
    open.value = false
  }
}, {
  label: t('nav.settings'),
  to: '/settings',
  icon: 'i-lucide-settings',
  defaultOpen: false,
  type: 'trigger',
  children: [{
    label: t('nav.general'),
    to: '/settings',
    exact: true,
    onSelect: () => {
      open.value = false
    }
  }, {
    label: t('nav.access'),
    to: '/settings/members',
    onSelect: () => {
      open.value = false
    }
  }, {
    label: t('nav.notifications'),
    to: '/settings/notifications',
    onSelect: () => {
      open.value = false
    }
  }, {
    label: t('nav.security'),
    to: '/settings/security',
    onSelect: () => {
      open.value = false
    }
  }]
}], [{
  label: t('nav.feedback'),
  icon: 'i-lucide-message-circle',
  to: 'https://github.com/mindevis/qstack/issues',
  target: '_blank'
}, {
  label: t('nav.help'),
  icon: 'i-lucide-info',
  to: 'https://github.com/mindevis/qstack',
  target: '_blank'
}]] satisfies NavigationMenuItem[][])

const groups = computed(() => [{
  id: 'links',
  label: t('nav.goTo'),
  items: links.value.flat()
}, {
  id: 'code',
  label: t('nav.code'),
  items: [{
    id: 'source',
    label: t('nav.viewSource'),
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
