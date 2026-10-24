<script setup lang="ts">
import type { Disk, Instance, Period, Range, Stat } from '~/types'

const props = defineProps<{
  period: Period
  range: Range
}>()

const [{ data: instances }, { data: disks }] = await Promise.all([
  useFetch<Instance[]>('/api/instances', { default: () => [] }),
  useFetch<Disk[]>('/api/disks', { default: () => [] })
])

// Current project usage; the variation over the period is demo data until
// metering exists (spec 09).
const { data: stats } = await useAsyncData<Stat[]>('stats', async () => {
  const running = instances.value.filter(i => i.status === 'running')
  return [{
    title: 'Instances',
    icon: 'i-lucide-server',
    value: `${running.length} / ${instances.value.length}`,
    to: '/compute/instances'
  }, {
    title: 'vCPU',
    icon: 'i-lucide-cpu',
    value: running.reduce((sum, i) => sum + i.vcpus, 0),
    to: '/compute/instances'
  }, {
    title: 'Memory, GiB',
    icon: 'i-lucide-memory-stick',
    value: running.reduce((sum, i) => sum + i.memoryGib, 0),
    to: '/compute/instances'
  }, {
    title: 'Disks, GiB',
    icon: 'i-lucide-hard-drive',
    value: disks.value.reduce((sum, d) => sum + d.sizeGib, 0),
    to: '/disks'
  }].map(stat => ({ ...stat, variation: randomInt(-15, 25) }))
}, {
  watch: [() => props.period, () => props.range],
  default: () => []
})
</script>

<template>
  <UPageGrid class="lg:grid-cols-4 gap-4 sm:gap-6 lg:gap-px">
    <UPageCard
      v-for="(stat, index) in stats"
      :key="index"
      :icon="stat.icon"
      :title="stat.title"
      :to="stat.to"
      variant="subtle"
      :ui="{
        container: 'gap-y-1.5',
        wrapper: 'items-start',
        leading: 'p-2.5 rounded-full bg-primary/10 ring ring-inset ring-primary/25 flex-col',
        title: 'font-normal text-muted text-xs uppercase'
      }"
      class="lg:rounded-none first:rounded-l-lg last:rounded-r-lg hover:z-1"
    >
      <div class="flex items-center gap-2">
        <span class="text-2xl font-semibold text-highlighted">
          {{ stat.value }}
        </span>

        <UBadge
          :color="stat.variation > 0 ? 'success' : 'error'"
          variant="subtle"
          class="text-xs"
        >
          {{ stat.variation > 0 ? '+' : '' }}{{ stat.variation }}%
        </UBadge>
      </div>
    </UPageCard>
  </UPageGrid>
</template>
