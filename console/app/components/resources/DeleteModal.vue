<script setup lang="ts">
const props = withDefaults(defineProps<{
  count?: number
  // Resource kind in singular, e.g. "instance".
  kind?: string
}>(), {
  count: 0,
  kind: 'resource'
})

const open = ref(false)
const toast = useToast()

async function onSubmit() {
  await new Promise(resolve => setTimeout(resolve, 1000))
  toast.add({ title: 'Demo', description: `Deleting ${props.count} ${props.kind}(s) needs the API (phase 1)`, color: 'info' })
  open.value = false
}
</script>

<template>
  <UModal
    v-model:open="open"
    :title="`Delete ${count} ${kind}${count > 1 ? 's' : ''}`"
    :description="`Are you sure, this action cannot be undone.`"
  >
    <slot />

    <template #body>
      <div class="flex justify-end gap-2">
        <UButton
          label="Cancel"
          color="neutral"
          variant="subtle"
          @click="open = false"
        />
        <UButton
          label="Delete"
          color="error"
          variant="solid"
          loading-auto
          @click="onSubmit"
        />
      </div>
    </template>
  </UModal>
</template>
