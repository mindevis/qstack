<script setup lang="ts">
const state = reactive<{ [key: string]: boolean }>({
  email: true,
  desktop: false,
  product_updates: true,
  weekly_digest: false,
  important_updates: true
})

const { t } = useI18n()

const sections = computed(() => [{
  title: t('settings.notifications.channels'),
  description: t('settings.notifications.channelsHint'),
  fields: [{
    name: 'email',
    label: t('settings.notifications.email'),
    description: t('settings.notifications.emailHint')
  }, {
    name: 'desktop',
    label: t('settings.notifications.desktop'),
    description: t('settings.notifications.desktopHint')
  }]
}, {
  title: t('settings.notifications.updates'),
  description: t('settings.notifications.updatesHint'),
  fields: [{
    name: 'weekly_digest',
    label: t('settings.notifications.operations'),
    description: t('settings.notifications.operationsHint')
  }, {
    name: 'product_updates',
    label: t('settings.notifications.quotas'),
    description: t('settings.notifications.quotasHint')
  }, {
    name: 'important_updates',
    label: t('settings.notifications.security'),
    description: t('settings.notifications.securityHint')
  }]
}])

async function onChange() {
  // Do something with data
  console.log(state)
}
</script>

<template>
  <div v-for="(section, index) in sections" :key="index">
    <UPageCard
      :title="section.title"
      :description="section.description"
      variant="naked"
      class="mb-4"
    />

    <UPageCard variant="subtle" :ui="{ container: 'divide-y divide-default' }">
      <UFormField
        v-for="field in section.fields"
        :key="field.name"
        :name="field.name"
        :label="field.label"
        :description="field.description"
        class="flex items-center justify-between not-last:pb-4 gap-2"
      >
        <USwitch
          v-model="state[field.name]"
          @update:model-value="onChange"
        />
      </UFormField>
    </UPageCard>
  </div>
</template>
