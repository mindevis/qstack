<script setup lang="ts">
import type { Operation } from '~/types'

const props = defineProps<{
  operation: Operation
}>()

const emits = defineEmits(['close'])

const toast = useToast()
const { t } = useI18n()
const { dateTime } = useFormat()

const status = computed(() => {
  if (!props.operation.done) return { label: t('status.inProgress'), color: 'info' as const }
  if (props.operation.error) return { label: t('status.failed'), color: 'error' as const }
  return { label: t('status.done'), color: 'success' as const }
})

function copy(value: string) {
  navigator.clipboard.writeText(value)
  toast.add({ title: t('common.copied'), description: value })
}

function onCancel() {
  toast.add({ title: t('app.demo'), description: t('operations.cancelDemo'), color: 'info' })
}
</script>

<template>
  <UDashboardPanel id="operations-2">
    <UDashboardNavbar :title="operation.description" :toggle="false">
      <template #leading>
        <UButton
          icon="i-lucide-x"
          color="neutral"
          variant="ghost"
          class="-ms-1.5"
          @click="emits('close')"
        />
      </template>

      <template #right>
        <UTooltip :text="t('operations.copyId')">
          <UButton
            icon="i-lucide-copy"
            color="neutral"
            variant="ghost"
            @click="copy(operation.id)"
          />
        </UTooltip>

        <UButton
          v-if="!operation.done"
          :label="t('operations.cancel')"
          color="neutral"
          variant="outline"
          icon="i-lucide-circle-stop"
          @click="onCancel"
        />
      </template>
    </UDashboardNavbar>

    <div class="flex flex-col sm:flex-row justify-between gap-1 p-4 sm:px-6 border-b border-default">
      <div class="flex items-start gap-4 sm:my-1.5">
        <UAvatar
          :alt="operation.createdBy"
          size="3xl"
        />

        <div class="min-w-0">
          <p class="font-semibold text-highlighted">
            {{ operation.createdBy }}
          </p>
          <p class="text-muted font-mono text-xs">
            {{ operation.id }}
          </p>
        </div>
      </div>

      <div class="max-sm:pl-16 flex items-center gap-3 sm:mt-2">
        <UBadge :color="status.color" variant="subtle">
          {{ status.label }}
        </UBadge>
        <span class="text-muted text-sm">{{ dateTime(operation.createTime) }}</span>
      </div>
    </div>

    <div class="flex-1 p-4 sm:p-6 overflow-y-auto space-y-4 text-sm">
      <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2">
        <dt class="text-muted">
          {{ t('common.resource') }}
        </dt>
        <dd class="font-mono">
          {{ operation.resourceId }}
        </dd>
        <dt class="text-muted">
          {{ t('common.startedBy') }}
        </dt>
        <dd>{{ operation.createdBy }}</dd>
        <dt class="text-muted">
          {{ t('operations.done') }}
        </dt>
        <dd>{{ operation.done ? t('common.yes') : t('common.no') }}</dd>
      </dl>

      <UAlert
        v-if="operation.error"
        color="error"
        variant="subtle"
        icon="i-lucide-circle-x"
        :title="t('operations.failedTitle')"
        :description="operation.error"
      />
    </div>
  </UDashboardPanel>
</template>
