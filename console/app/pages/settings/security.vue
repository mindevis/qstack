<script setup lang="ts">
import * as z from 'zod'
import type { FormError } from '@nuxt/ui'

const { t } = useI18n()

// Passwords: at least 12 characters (REQ-IAM-020).
const passwordSchema = z.object({
  current: z.string().min(12, t('settings.security.min')),
  new: z.string().min(12, t('settings.security.min'))
})

type PasswordSchema = z.output<typeof passwordSchema>

const password = reactive<Partial<PasswordSchema>>({
  current: '',
  new: ''
})

const validate = (state: Partial<PasswordSchema>): FormError[] => {
  const errors: FormError[] = []
  if (state.current && state.new && state.current === state.new) {
    errors.push({ name: 'new', message: t('settings.security.different') })
  }
  return errors
}
</script>

<template>
  <UPageCard
    :title="t('settings.security.password')"
    :description="t('settings.security.passwordHint')"
    variant="subtle"
  >
    <UForm
      :schema="passwordSchema"
      :state="password"
      :validate="validate"
      class="flex flex-col gap-4 max-w-xs"
    >
      <UFormField name="current">
        <UInput
          v-model="password.current"
          type="password"
          :placeholder="t('settings.security.current')"
          class="w-full"
        />
      </UFormField>

      <UFormField name="new">
        <UInput
          v-model="password.new"
          type="password"
          :placeholder="t('settings.security.new')"
          class="w-full"
        />
      </UFormField>

      <UButton :label="t('settings.security.update')" class="w-fit" type="submit" />
    </UForm>
  </UPageCard>

  <UPageCard
    :title="t('settings.security.account')"
    :description="t('settings.security.accountHint')"
    class="bg-linear-to-tl from-error/10 from-5% to-default"
  >
    <template #footer>
      <UButton :label="t('settings.security.deleteAccount')" color="error" />
    </template>
  </UPageCard>
</template>
