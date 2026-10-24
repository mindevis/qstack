<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { breakpointsTailwind } from '@vueuse/core'
import type { Operation } from '~/types'

const tabItems = [{
  label: 'All',
  value: 'all'
}, {
  label: 'Running',
  value: 'running'
}, {
  label: 'Failed',
  value: 'failed'
}]
const selectedTab = ref('all')

const { data: operations } = await useFetch<Operation[]>('/api/operations', { default: () => [] })

// Filter operations based on the selected tab
const filteredOperations = computed(() => {
  if (selectedTab.value === 'running') {
    return operations.value.filter(op => !op.done)
  }
  if (selectedTab.value === 'failed') {
    return operations.value.filter(op => !!op.error)
  }

  return operations.value
})

const selectedOperation = ref<Operation | null>()

const isOperationPanelOpen = computed({
  get() {
    return !!selectedOperation.value
  },
  set(value: boolean) {
    if (!value) {
      selectedOperation.value = null
    }
  }
})

// Reset the selection if it is not in the filtered operations
watch(filteredOperations, () => {
  if (!filteredOperations.value.find(op => op.id === selectedOperation.value?.id)) {
    selectedOperation.value = null
  }
})

const breakpoints = useBreakpoints(breakpointsTailwind)
const isMobile = breakpoints.smaller('lg')
</script>

<template>
  <UDashboardPanel
    id="operations-1"
    :default-size="30"
    :min-size="20"
    :max-size="40"
    resizable
  >
    <UDashboardNavbar title="Operations">
      <template #leading>
        <UDashboardSidebarCollapse />
      </template>
      <template #trailing>
        <UBadge :label="filteredOperations.length" variant="subtle" />
      </template>

      <template #right>
        <UTabs
          v-model="selectedTab"
          :items="tabItems"
          :content="false"
          size="xs"
        />
      </template>
    </UDashboardNavbar>
    <OperationsList v-model="selectedOperation" :operations="filteredOperations" />
  </UDashboardPanel>

  <OperationsDetail v-if="selectedOperation" :operation="selectedOperation" @close="selectedOperation = null" />
  <div v-else class="hidden lg:flex flex-1 items-center justify-center">
    <UIcon name="i-lucide-list-checks" class="size-32 text-dimmed" />
  </div>

  <ClientOnly>
    <USlideover v-if="isMobile" v-model:open="isOperationPanelOpen">
      <template #content>
        <OperationsDetail v-if="selectedOperation" :operation="selectedOperation" @close="selectedOperation = null" />
      </template>
    </USlideover>
  </ClientOnly>
</template>
