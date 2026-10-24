<script setup lang="ts">
import { eachDayOfInterval, eachWeekOfInterval, eachMonthOfInterval, format } from 'date-fns'
import { enUS, ru } from 'date-fns/locale'
import type { Period, Range } from '~/types'

const props = defineProps<{
  period: Period
  range: Range
}>()

const { t, locale } = useI18n()

type DataRecord = {
  date: string
  amount: number
}

// Demo: vCPU-hours consumed by the project (metering, spec 09).
const formatNumber = (value: number) => new Intl.NumberFormat(locale.value, { maximumFractionDigits: 0 }).format(value)

const formatDate = (date: Date): string => {
  const options = { locale: locale.value === 'ru' ? ru : enUS }
  return ({
    daily: format(date, 'd MMM', options),
    weekly: format(date, 'd MMM yyyy', options),
    monthly: format(date, 'MMM yyy', options)
  })[props.period]
}

const { data } = await useAsyncData<DataRecord[]>('chart', async () => {
  const dates = ({
    daily: eachDayOfInterval,
    weekly: eachWeekOfInterval,
    monthly: eachMonthOfInterval
  } as Record<Period, typeof eachDayOfInterval>)[props.period](props.range)

  // The y axis rounds the highest amount up to its next step, so keeping it just above 700 leaves room above the peak
  const amounts = dates.map(() => randomInt(100, 700))
  amounts[randomInt(0, amounts.length - 1)] = randomInt(710, 720)

  return dates.map((date, i) => ({ date: formatDate(date), amount: amounts[i]! }))
}, {
  watch: [() => props.period, () => props.range, locale],
  default: () => []
})

const total = computed(() => data.value.reduce((acc: number, { amount }) => acc + amount, 0))
</script>

<template>
  <UCard :ui="{ root: 'overflow-visible', body: 'p-0! overflow-hidden rounded-b-[inherit]' }">
    <template #header>
      <div>
        <p class="text-xs text-muted uppercase mb-1.5">
          {{ t('home.vcpuHours') }}
        </p>
        <p class="text-3xl text-highlighted font-semibold">
          {{ formatNumber(total) }}
        </p>
      </div>
    </template>

    <UChart
      type="area"
      curve="monotone"
      :data="data"
      index="date"
      :categories="[{ key: 'amount', label: t('home.vcpuHours') }]"
      :x-axis="false"
      :y-axis="false"
      :grid="true"
      :format="formatNumber"
      :aspect-ratio="3"
      :initial-width="1152"
      class="-my-px"
      :aria-label="t('home.vcpuHours')"
    />
  </UCard>
</template>
