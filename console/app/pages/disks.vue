<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { getPaginationRowModel } from '@tanstack/table-core'
import type { Disk } from '~/types'

const UBadge = resolveComponent('UBadge')
const UCheckbox = resolveComponent('UCheckbox')

const toast = useToast()
const table = useTemplateRef('table')

const columnFilters = ref([{
  id: 'name',
  value: ''
}])
const rowSelection = ref({})

const { data, status } = await useFetch<Disk[]>('/api/disks', {
  lazy: true
})

const statusColor = {
  ready: 'success' as const,
  creating: 'info' as const,
  error: 'error' as const
}

const columns: TableColumn<Disk>[] = [
  {
    id: 'select',
    header: ({ table }) =>
      h(UCheckbox, {
        'modelValue': table.getIsSomePageRowsSelected()
          ? 'indeterminate'
          : table.getIsAllPageRowsSelected(),
        'onUpdate:modelValue': (value: boolean | 'indeterminate') =>
          table.toggleAllPageRowsSelected(!!value),
        'ariaLabel': 'Select all'
      }),
    cell: ({ row }) =>
      h(UCheckbox, {
        'modelValue': row.getIsSelected(),
        'onUpdate:modelValue': (value: boolean | 'indeterminate') => row.toggleSelected(!!value),
        'ariaLabel': 'Select row'
      })
  },
  {
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => h('div', undefined, [
      h('p', { class: 'font-medium text-highlighted' }, row.original.name),
      h('p', { class: 'font-mono text-xs' }, row.original.id)
    ])
  },
  {
    accessorKey: 'status',
    header: 'Status',
    cell: ({ row }) => h(UBadge, { class: 'capitalize', variant: 'subtle', color: statusColor[row.original.status] }, () => row.original.status)
  },
  {
    accessorKey: 'type',
    header: 'Type'
  },
  {
    accessorKey: 'sizeGib',
    header: 'Size',
    cell: ({ row }) => `${row.original.sizeGib} GiB`
  },
  {
    accessorKey: 'attachedTo',
    header: 'Attached to',
    cell: ({ row }) => row.original.attachedTo ?? '—'
  },
  {
    accessorKey: 'zone',
    header: 'Zone'
  }
]

const name = computed({
  get: (): string => (table.value?.tableApi?.getColumn('name')?.getFilterValue() as string) || '',
  set: (value: string) => table.value?.tableApi?.getColumn('name')?.setFilterValue(value || undefined)
})

const pagination = ref({
  pageIndex: 0,
  pageSize: 10
})

function onCreate() {
  toast.add({ title: 'Demo', description: 'Disks are created by the Block Storage API (phase 1)', color: 'info' })
}
</script>

<template>
  <UDashboardPanel id="disks">
    <template #header>
      <UDashboardNavbar title="Disks">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>

        <template #right>
          <UButton label="New disk" icon="i-lucide-plus" @click="onCreate" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div class="flex flex-wrap items-center justify-between gap-1.5">
        <UInput
          v-model="name"
          class="max-w-sm"
          icon="i-lucide-search"
          placeholder="Filter names..."
        />

        <ResourcesDeleteModal kind="disk" :count="table?.tableApi?.getFilteredSelectedRowModel().rows.length">
          <UButton
            v-if="table?.tableApi?.getFilteredSelectedRowModel().rows.length"
            label="Delete"
            color="error"
            variant="subtle"
            icon="i-lucide-trash"
          >
            <template #trailing>
              <UKbd>
                {{ table?.tableApi?.getFilteredSelectedRowModel().rows.length }}
              </UKbd>
            </template>
          </UButton>
        </ResourcesDeleteModal>
      </div>

      <UTable
        ref="table"
        v-model:column-filters="columnFilters"
        v-model:row-selection="rowSelection"
        v-model:pagination="pagination"
        :pagination-options="{
          getPaginationRowModel: getPaginationRowModel()
        }"
        class="shrink-0"
        :data="data"
        :columns="columns"
        :loading="status === 'pending'"
        :ui="{
          base: 'table-fixed border-separate border-spacing-0',
          thead: '[&>tr]:bg-elevated/50 [&>tr]:after:content-none',
          tbody: '[&>tr]:last:[&>td]:border-b-0',
          th: 'py-2 first:rounded-l-lg last:rounded-r-lg border-y border-default first:border-l last:border-r',
          td: 'border-b border-default',
          separator: 'h-0'
        }"
      />

      <div class="flex items-center justify-between gap-3 border-t border-default pt-4 mt-auto">
        <div class="text-sm text-muted">
          {{ table?.tableApi?.getFilteredSelectedRowModel().rows.length || 0 }} of
          {{ table?.tableApi?.getFilteredRowModel().rows.length || 0 }} row(s) selected.
        </div>

        <UPagination
          :default-page="(table?.tableApi?.getState().pagination.pageIndex || 0) + 1"
          :items-per-page="table?.tableApi?.getState().pagination.pageSize"
          :total="table?.tableApi?.getFilteredRowModel().rows.length"
          @update:page="(p: number) => table?.tableApi?.setPageIndex(p - 1)"
        />
      </div>
    </template>
  </UDashboardPanel>
</template>
