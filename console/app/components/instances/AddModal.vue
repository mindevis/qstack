<script setup lang="ts">
import * as z from 'zod'
import type { FormSubmitEvent } from '@nuxt/ui'

const { t } = useI18n()

// Resource names: ^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ (spec 02 §3).
const schema = z.object({
  name: z.string().regex(/^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/, t('instances.nameRule')),
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
  toast.add({ title: t('app.demo'), description: t('instances.createDemo', { name: event.data.name }), color: 'info' })
  open.value = false
}
</script>

<template>
  <UModal v-model:open="open" :title="t('instances.new')" :description="t('instances.newDescription')">
    <UButton :label="t('instances.new')" icon="i-lucide-plus" />

    <template #body>
      <UForm
        :schema="schema"
        :state="state"
        class="space-y-4"
        @submit="onSubmit"
      >
        <UFormField :label="t('common.name')" placeholder="web-1" name="name">
          <UInput v-model="state.name" class="w-full" />
        </UFormField>
        <UFormField :label="t('instances.instanceType')" name="type">
          <USelect v-model="state.type" :items="['s1.small', 's1.medium', 's1.large', 's1.xlarge']" class="w-full" />
        </UFormField>
        <UFormField :label="t('common.zone')" name="zone">
          <USelect v-model="state.zone" :items="['ru-central1-a']" class="w-full" />
        </UFormField>
        <div class="flex justify-end gap-2">
          <UButton
            :label="t('common.cancel')"
            color="neutral"
            variant="subtle"
            @click="open = false"
          />
          <UButton
            :label="t('common.create')"
            color="primary"
            variant="solid"
            type="submit"
          />
        </div>
      </UForm>
    </template>
  </UModal>
</template>
