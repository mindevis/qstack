<script setup lang="ts">
import { format, isToday } from 'date-fns'
import { enUS, ru } from 'date-fns/locale'
import type { Operation } from '~/types'

const props = defineProps<{
  operations: Operation[]
}>()

const { t, locale } = useI18n()
const dateLocale = computed(() => ({ locale: locale.value === 'ru' ? ru : enUS }))

const operationsRefs = ref<Record<string, Element | null>>({})

const selectedOperation = defineModel<Operation | null>()

watch(selectedOperation, () => {
  if (!selectedOperation.value) {
    return
  }
  const ref = operationsRefs.value[selectedOperation.value.id]
  if (ref) {
    ref.scrollIntoView({ block: 'nearest' })
  }
})

defineShortcuts({
  arrowdown: () => {
    const index = props.operations.findIndex((op: Operation) => op.id === selectedOperation.value?.id)

    if (index === -1) {
      selectedOperation.value = props.operations[0]
    } else if (index < props.operations.length - 1) {
      selectedOperation.value = props.operations[index + 1]
    }
  },
  arrowup: () => {
    const index = props.operations.findIndex((op: Operation) => op.id === selectedOperation.value?.id)

    if (index === -1) {
      selectedOperation.value = props.operations[props.operations.length - 1]
    } else if (index > 0) {
      selectedOperation.value = props.operations[index - 1]
    }
  }
})
</script>

<template>
  <div class="overflow-y-auto divide-y divide-default">
    <div
      v-for="(op, index) in operations"
      :key="index"
      :ref="(el) => { operationsRefs[op.id] = el as Element | null }"
    >
      <div
        class="p-4 sm:px-6 text-sm cursor-pointer border-l-2 transition-colors"
        :class="[
          !op.done ? 'text-highlighted' : 'text-toned',
          selectedOperation && selectedOperation.id === op.id
            ? 'border-primary bg-primary/10'
            : 'border-bg hover:border-primary hover:bg-primary/5'
        ]"
        @click="selectedOperation = op"
      >
        <div class="flex items-center justify-between" :class="[!op.done && 'font-semibold']">
          <div class="flex items-center gap-3">
            {{ op.createdBy }}

            <UChip v-if="!op.done" color="info" />
            <UChip v-else-if="op.error" color="error" />
          </div>

          <span>{{ isToday(new Date(op.createTime)) ? format(new Date(op.createTime), 'HH:mm', dateLocale) : format(new Date(op.createTime), 'dd MMM', dateLocale) }}</span>
        </div>
        <p class="truncate" :class="[!op.done && 'font-semibold']">
          {{ op.description }}
        </p>
        <p class="text-dimmed line-clamp-1">
          {{ op.error ?? (op.done ? t('operations.done') : t('operations.runningNow')) }}
        </p>
      </div>
    </div>
  </div>
</template>
