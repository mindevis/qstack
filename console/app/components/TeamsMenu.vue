<script setup lang="ts">
import type { DropdownMenuItem } from '@nuxt/ui'

defineProps<{
  collapsed?: boolean
}>()

// Organization / project switcher (was the template's team switcher).
const { data: session } = await useFetch('/api/session', { key: 'session' })

const organization = computed(() => session.value?.organizations[0])
const projects = computed(() => organization.value?.projects ?? [])
const selectedProjectId = useState('project', () => projects.value[0]?.id)
const selectedProject = computed(() => projects.value.find(p => p.id === selectedProjectId.value) ?? projects.value[0])

const items = computed<DropdownMenuItem[][]>(() => {
  return [[{
    type: 'label',
    label: organization.value?.name
  }], projects.value.map(project => ({
    label: project.name,
    icon: 'i-lucide-folder',
    type: 'checkbox' as const,
    checked: project.id === selectedProject.value?.id,
    onSelect() {
      selectedProjectId.value = project.id
    }
  })), [{
    label: 'Create project',
    icon: 'i-lucide-circle-plus',
    disabled: true
  }, {
    label: 'Manage organization',
    icon: 'i-lucide-cog',
    disabled: true
  }]]
})
</script>

<template>
  <UDropdownMenu
    :items="items"
    :content="{ align: 'center', collisionPadding: 12 }"
    :ui="{ content: collapsed ? 'w-40' : 'w-(--reka-dropdown-menu-trigger-width)' }"
  >
    <UButton
      v-bind="{
        icon: 'i-lucide-cloud',
        label: collapsed ? undefined : `${organization?.name} / ${selectedProject?.name}`,
        trailingIcon: collapsed ? undefined : 'i-lucide-chevrons-up-down'
      }"
      color="neutral"
      variant="ghost"
      block
      :square="collapsed"
      class="data-[state=open]:bg-elevated"
      :class="[!collapsed && 'py-2']"
      :ui="{
        leadingIcon: 'text-primary',
        trailingIcon: 'text-dimmed'
      }"
    />
  </UDropdownMenu>
</template>
