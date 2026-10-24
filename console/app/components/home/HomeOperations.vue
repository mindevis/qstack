<script setup lang="ts">
import { h, resolveComponent } from 'vue'
import type { TableColumn } from '@nuxt/ui'
import type { Operation } from '~/types'

const UBadge = resolveComponent('UBadge')

// The latest operations of the project.
const { data } = await useFetch<Operation[]>('/api/operations', {
  default: () => [],
  transform: ops => ops.slice(0, 5)
})

const columns: TableColumn<Operation>[] = [
  {
    accessorKey: 'description',
    header: 'Operation'
  },
  {
    accessorKey: 'createTime',
    header: 'Date',
    cell: ({ row }) => {
      return new Date(row.getValue('createTime')).toLocaleString('en-US', {
        day: 'numeric',
        month: 'short',
        hour: '2-digit',
        minute: '2-digit',
        hour12: false
      })
    }
  },
  {
    id: 'status',
    header: 'Status',
    cell: ({ row }) => {
      const op = row.original
      const [label, color] = !op.done
        ? ['running', 'info' as const]
        : op.error ? ['failed', 'error' as const] : ['done', 'success' as const]

      return h(UBadge, { class: 'capitalize', variant: 'subtle', color }, () => label)
    }
  },
  {
    accessorKey: 'createdBy',
    header: 'Started by'
  },
  {
    accessorKey: 'resourceId',
    header: () => h('div', { class: 'text-right' }, 'Resource'),
    cell: ({ row }) => h('div', { class: 'text-right font-mono text-xs' }, row.original.resourceId)
  }
]
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
