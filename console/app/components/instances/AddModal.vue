<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'

// Resource names: ^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ (spec 02 §3).
const schema = z.object({
  name: z.string().regex(/^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/, 'Lowercase letters, digits and hyphens; starts with a letter'),
  type: z.string(),
  zone: z.string()
})
const open = ref(false)

type Schema = z.output<typeof schema>

const state = reactive<Partial<Schema>>({
  name: '',
  type: 's1.small',
  zone: 'ru-central1-a'
})

const toast = useToast()
async function onSubmit(event: FormSubmitEvent<Schema>) {
  toast.add({ title: 'Demo', description: `Instance ${event.data.name} would be created by the Compute API (phase 1)`, color: 'info' })
  open.value = false
}
</script>

<template>
  <UModal v-model:open="open" title="New instance" description="Create a virtual machine in the project">
    <UButton label="New instance" icon="i-lucide-plus" />

    <template #body>
      <UForm
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField label="Name" placeholder="web-1" name="name">
          <UInput v-model="state.name" class="w-full" />
        </UFormField>
        <UFormField label="Instance type" name="type">
          <USelect v-model="state.type" :items="['s1.small', 's1.medium', 's1.large', 's1.xlarge']" class="w-full" />
        </UFormField>
        <UFormField label="Zone" name="zone">
          <USelect v-model="state.zone" :items="['ru-central1-a']" class="w-full" />
        </UFormField>
        <div class="flex justify-end gap-2">
          <UButton
            label="Cancel"
            color="neutral"
            variant="subtle"
            @click="open = false"
          />
          <UButton
            label="Create"
            color="primary"
            variant="solid"
            type="submit"
          />
        </div>
      </UForm>
    </template>
  </UModal>
</template>
