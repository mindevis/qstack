<script setup lang="ts">
import { h, resolveComponent } from 'vue'
import type { TableColumn } from '@nuxt/ui'
import type { Operation } from '~/types'

const UBadge = resolveComponent('UBadge')
const { t } = useI18n()
const { dateTime } = useFormat()

// The latest operations of the project.
const { data } = await useFetch<Operation[]>('/api/operations', {
  default: () => [],
  transform: ops => ops.slice(0, 5)
})

const columns = computed<TableColumn<Operation>[]>(() => [
  {
    accessorKey: 'description',
    header: t('common.operation')
  },
  {
    accessorKey: 'createTime',
    header: t('common.date'),
    cell: ({ row }) => dateTime(row.original.createTime)
  },
  {
    id: 'status',
    header: t('common.status'),
    cell: ({ row }) => {
      const op = row.original
      const [label, color] = !op.done
        ? ['inProgress', 'info' as const]
        : op.error ? ['failed', 'error' as const] : ['done', 'success' as const]

      return h(UBadge, { variant: 'subtle', color }, () => t(`status.${label}`))
    }
  },
  {
    accessorKey: 'createdBy',
    header: t('common.startedBy')
  },
  {
    accessorKey: 'resourceId',
    header: () => h('div', { class: 'text-right' }, t('common.resource')),
    cell: ({ row }) => h('div', { class: 'text-right font-mono text-xs' }, row.original.resourceId)
  }
])
</script>

<template>
  <UTable
    :data="data"
    :columns="columns"
    class="shrink-0"
    :ui="{
      base: 'table-fixed border-separate border-spacing-0',
      thead: '[&>tr]:bg-elevated/50 [&>tr]:after:content-none',
      tbody: '[&>tr]:last:[&>td]:border-b-0',
      th: 'first:rounded-l-lg last:rounded-r-lg border-y border-default first:border-l last:border-r',
      td: 'border-b border-default'
    }"
  />
</template>
