<script setup lang="ts">
import { formatTimeAgo } from '@vueuse/core'
import type { ClusterNode } from '~/types'

// Compute nodes of the region (spec 15 §4, roles ops.*).
const { data: nodes } = await useFetch<ClusterNode[]>('/api/nodes', { default: () => [] })

const statusColor = {
  ready: 'success' as const,
  maintenance: 'warning' as const,
  unreachable: 'error' as const
}

const pct = (u: { used: number, total: number }) => Math.round((u.used / u.total) * 100)
</script>

<template>
  <UDashboardPanel id="cluster">
    <template #header>
      <UDashboardNavbar title="Cluster">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>

        <template #right>
          <UBadge variant="subtle" color="neutral" icon="i-lucide-layers">
            profile: single
          </UBadge>
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <UPageGrid class="lg:grid-cols-3 gap-4 sm:gap-6">
        <UPageCard
          v-for="node in nodes"
          :key="node.id"
          icon="i-lucide-server-cog"
          :title="node.name"
          :description="node.id"
          variant="subtle"
          :ui="{
            leading: 'p-2.5 rounded-full bg-primary/10 ring ring-inset ring-primary/25 flex-col',
            description: 'font-mono text-xs'
          }"
        >
          <div class="space-y-3 text-sm">
            <div class="flex items-center justify-between">
              <UBadge :color="statusColor[node.status]" variant="subtle" class="capitalize">
                {{ node.status }}
              </UBadge>
              <code class="text-muted">{{ node.version }}</code>
            </div>

            <div>
              <div class="flex justify-between text-muted">
                <span>vCPU</span>
                <span class="tabular-nums">{{ node.vcpus.used }} / {{ node.vcpus.total }}</span>
              </div>
              <UProgress :model-value="pct(node.vcpus)" size="sm" class="mt-1" />
            </div>

            <div>
              <div class="flex justify-between text-muted">
                <span>Memory</span>
                <span class="tabular-nums">{{ node.memoryGib.used }} / {{ node.memoryGib.total }} GiB</span>
              </div>
              <UProgress :model-value="pct(node.memoryGib)" size="sm" class="mt-1" />
            </div>

            <div class="flex justify-between text-muted">
              <span>{{ node.instances }} instances · {{ node.zone }}</span>
              <span>heartbeat {{ formatTimeAgo(new Date(node.lastHeartbeat)) }}</span>
            </div>
          </div>
        </UPageCard>
      </UPageGrid>
    </template>
  </UDashboardPanel>
</template>
