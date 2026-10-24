<script setup lang="ts">
import { formatTimeAgo } from '@vueuse/core'
import type { Network } from '~/types'

const toast = useToast()
const { data: networks } = await useFetch<Network[]>('/api/networks', { default: () => [] })

const q = ref('')
const filtered = computed(() => networks.value.filter(n => n.name.includes(q.value.toLowerCase())))

function onCreate() {
  toast.add({ title: 'Demo', description: 'Networks are created by the VPC API (phase 1)', color: 'info' })
}
</script>

<template>
  <UDashboardPanel id="networks">
    <template #header>
      <UDashboardNavbar title="Cloud networks">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>

        <template #right>
          <UButton label="New network" icon="i-lucide-plus" @click="onCreate" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <UInput
        v-model="q"
        class="max-w-sm"
        icon="i-lucide-search"
        placeholder="Filter networks..."
      />

      <UPageCard
        v-for="network in filtered"
        :key="network.id"
        variant="subtle"
        :ui="{ container: 'p-0 sm:p-0 gap-y-0', wrapper: 'items-stretch', header: 'p-4 mb-0 border-b border-default' }"
      >
        <template #header>
          <div class="flex items-center gap-3">
            <UIcon name="i-lucide-network" class="size-5 text-primary" />
            <div>
              <p class="font-medium text-highlighted">
                {{ network.name }}
              </p>
              <p class="font-mono text-xs text-muted">
                {{ network.id }}
              </p>
            </div>
            <span class="ms-auto text-xs text-muted">Created {{ formatTimeAgo(new Date(network.createTime)) }}</span>
          </div>
        </template>

        <ul role="list" class="divide-y divide-default">
          <li
            v-for="subnet in network.subnets"
            :key="subnet.id"
            class="flex items-center justify-between gap-3 py-3 px-4 sm:px-6 text-sm"
          >
            <div class="min-w-0">
              <p class="text-highlighted font-medium truncate">
                {{ subnet.name }}
              </p>
              <p class="font-mono text-xs text-muted truncate">
                {{ subnet.id }}
              </p>
            </div>
            <div class="flex items-center gap-3">
              <UBadge variant="subtle" color="neutral">
                {{ subnet.zone }}
              </UBadge>
              <code class="text-primary">{{ subnet.cidr }}</code>
            </div>
          </li>
        </ul>
      </UPageCard>
    </template>
  </UDashboardPanel>
</template>
