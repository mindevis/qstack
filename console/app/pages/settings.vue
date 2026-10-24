<script setup lang="ts">
import type { NavigationMenuItem } from '@nuxt/ui'

const { t } = useI18n()

const links = computed(() => [[{
  label: t('nav.general'),
  icon: 'i-lucide-user',
  to: '/settings',
  exact: true
}, {
  label: t('nav.access'),
  icon: 'i-lucide-users',
  to: '/settings/members'
}, {
  label: t('nav.notifications'),
  icon: 'i-lucide-bell',
  to: '/settings/notifications'
}, {
  label: t('nav.security'),
  icon: 'i-lucide-shield',
  to: '/settings/security'
}], [{
  label: t('settings.docs'),
  icon: 'i-lucide-book-open',
  to: 'https://github.com/mindevis/qstack',
  target: '_blank'
}]] satisfies NavigationMenuItem[][])
</script>

<template>
  <UDashboardPanel id="settings" :ui="{ body: 'lg:py-12' }">
    <template #header>
      <UDashboardNavbar :title="t('settings.title')">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
      </UDashboardNavbar>

      <UDashboardToolbar>
        <!-- NOTE: The `-mx-1` class is used to align with the `DashboardSidebarCollapse` button here. -->
        <UNavigationMenu :items="links" highlight class="-mx-1 flex-1" />
      </UDashboardToolbar>
    </template>

    <template #body>
      <div class="flex flex-col gap-4 sm:gap-6 lg:gap-12 w-full lg:max-w-2xl mx-auto">
        <NuxtPage />
      </div>
    </template>
  </UDashboardPanel>
</template>
