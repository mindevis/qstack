<script setup lang="ts">
withDefaults(defineProps<{
  count?: number
  // Resource kind as shown in the title, e.g. "ВМ" / "instances".
  kind?: string
}>(), {
  count: 0,
  kind: 'resource'
})

const open = ref(false)
const toast = useToast()
const { t } = useI18n()

async function onSubmit() {
  await new Promise(resolve => setTimeout(resolve, 1000))
  toast.add({ title: t('app.demo'), description: t('delete.demo'), color: 'info' })
  open.value = false
}
</script>

<template>
  <UModal
    v-model:open="open"
    :title="t('delete.title', { kind, count })"
    :description="t('delete.description')"
  >
    <slot />

    <template #body>
      <div class="flex justify-end gap-2">
        <UButton
          :label="t('common.cancel')"
          color="neutral"
          variant="subtle"
          @click="open = false"
        />
        <UButton
          :label="t('common.delete')"
          color="error"
          variant="solid"
          loading-auto
          @click="onSubmit"
        />
      </div>
    </template>
  </UModal>
</template>
