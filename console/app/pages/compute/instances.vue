<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { upperFirst } from 'scule'
import { getPaginationRowModel } from '@tanstack/table-core'
import type { Row } from '@tanstack/table-core'
import { formatTimeAgo } from '@vueuse/core'
import type { Instance } from '~/types'

const UButton = resolveComponent('UButton')
const UBadge = resolveComponent('UBadge')
const UDropdownMenu = resolveComponent('UDropdownMenu')
const UCheckbox = resolveComponent('UCheckbox')

const toast = useToast()
const table = useTemplateRef('table')

const columnFilters = ref([{
  id: 'name',
  value: ''
}])
const columnVisibility = ref()
const rowSelection = ref({})

const { data, status } = await useFetch<Instance[]>('/api/instances', {
  lazy: true
})

function demo(action: string, row: Row<Instance>) {
  toast.add({
    title: 'Demo',
    description: `${action} ${row.original.name}: available with the Compute API (phase 1)`,
    color: 'info'
  })
}

function getRowItems(row: Row<Instance>) {
  return [
    {
      type: 'label',
      label: 'Actions'
    },
    {
      label: 'Copy instance ID',
      icon: 'i-lucide-copy',
      onSelect() {
        navigator.clipboard.writeText(row.original.id)
        toast.add({
          title: 'Copied to clipboard',
          description: 'Instance ID copied to clipboard'
        })
      }
    },
    {
      type: 'separator'
    },
    {
      label: 'Start',
      icon: 'i-lucide-play',
      disabled: row.original.status !== 'stopped',
      onSelect: () => demo('Start', row)
    },
    {
      label: 'Stop',
      icon: 'i-lucide-square',
      disabled: row.original.status !== 'running',
      onSelect: () => demo('Stop', row)
    },
    {
      label: 'Serial console',
      icon: 'i-lucide-terminal',
      onSelect: () => demo('Serial console of', row)
    },
    {
      type: 'separator'
    },
    {
      label: 'Delete instance',
      icon: 'i-lucide-trash',
      color: 'error',
      onSelect: () => demo('Delete', row)
    }
  ]
}

const statusColor = {
  running: 'success' as const,
  provisioning: 'info' as const,
  stopped: 'neutral' as const,
  error: 'error' as const
}

const columns: TableColumn<Instance>[] = [
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
    header: ({ column }) => {
      const isSorted = column.getIsSorted()

      return h(UButton, {
        color: 'neutral',
        variant: 'ghost',
        label: 'Name',
        icon: isSorted
          ? isSorted === 'asc'
            ? 'i-lucide-arrow-up-narrow-wide'
            : 'i-lucide-arrow-down-wide-narrow'
          : 'i-lucide-arrow-up-down',
        class: '-mx-2.5',
        onClick: () => column.toggleSorting(column.getIsSorted() === 'asc')
      })
    },
    cell: ({ row }) => {
      return h('div', undefined, [
        h('p', { class: 'font-medium text-highlighted' }, row.original.name),
        h('p', { class: 'font-mono text-xs' }, row.original.id)
      ])
    }
  },
  {
    accessorKey: 'status',
    header: 'Status',
    filterFn: 'equals',
    cell: ({ row }) => {
      return h(UBadge, { class: 'capitalize', variant: 'subtle', color: statusColor[row.original.status] }, () =>
        row.original.status
      )
    }
  },
  {
    accessorKey: 'type',
    header: 'Type',
    cell: ({ row }) => `${row.original.type} · ${row.original.vcpus} vCPU · ${row.original.memoryGib} GiB`
  },
  {
    accessorKey: 'zone',
    header: 'Zone'
  },
  {
    id: 'addresses',
    header: 'Addresses',
    cell: ({ row }) => h('div', { class: 'font-mono text-xs' }, [
      h('p', undefined, row.original.privateIp),
      row.original.publicIp ? h('p', { class: 'text-primary' }, row.original.publicIp) : null
    ])
  },
  {
    accessorKey: 'createTime',
    header: 'Created',
    cell: ({ row }) => formatTimeAgo(new Date(row.original.createTime))
  },
  {
    id: 'actions',
    cell: ({ row }) => {
      return h(
        'div',
        { class: 'text-right' },
        h(
          UDropdownMenu,
          {
            content: {
              align: 'end'
            },
            items: getRowItems(row)
          },
          () =>
            h(UButton, {
              icon: 'i-lucide-ellipsis-vertical',
              color: 'neutral',
              variant: 'ghost',
              class: 'ml-auto'
            })
        )
      )
    }
  }
]

const statusFilter = ref('all')

watch(() => statusFilter.value, (newVal) => {
  if (!table?.value?.tableApi) return

  const statusColumn = table.value.tableApi.getColumn('status')
  if (!statusColumn) return

  if (newVal === 'all') {
    statusColumn.setFilterValue(undefined)
  } else {
    statusColumn.setFilterValue(newVal)
  }
})

const name = computed({
  get: (): string => {
    return (table.value?.tableApi?.getColumn('name')?.getFilterValue() as string) || ''
  },
  set: (value: string) => {
    table.value?.tableApi?.getColumn('name')?.setFilterValue(value || undefined)
  }
})

const pagination = ref({
  pageIndex: 0,
  pageSize: 10
})
</script>

<template>
  <UDashboardPanel id="instances">
    <template #header>
      <UDashboardNavbar title="Instances">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>

        <template #right>
          <InstancesAddModal />
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

        <div class="flex flex-wrap items-center gap-1.5">
          <ResourcesDeleteModal kind="instance" :count="table?.tableApi?.getFilteredSelectedRowModel().rows.length">
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

          <USelect
            v-model="statusFilter"
            :items="[
              { label: 'All', value: 'all' },
              { label: 'Running', value: 'running' },
              { label: 'Provisioning', value: 'provisioning' },
              { label: 'Stopped', value: 'stopped' },
              { label: 'Error', value: 'error' }
            ]"
            :ui="{ trailingIcon: 'group-data-[state=open]:rotate-180 transition-transform duration-200' }"
            placeholder="Filter status"
            class="min-w-28"
          />
          <UDropdownMenu
            :items="
              table?.tableApi
                ?.getAllColumns()
                .filter((column: any) => column.getCanHide())
                .map((column: any) => ({
                  label: upperFirst(column.id),
                  type: 'checkbox' as const,
                  checked: column.getIsVisible(),
                  onUpdateChecked(checked: boolean) {
                    table?.tableApi?.getColumn(column.id)?.toggleVisibility(!!checked)
                  },
                  onSelect(e?: Event) {
                    e?.preventDefault()
                  }
                }))
            "
            :content="{ align: 'end' }"
          >
            <UButton
              label="Display"
              color="neutral"
              variant="outline"
              trailing-icon="i-lucide-settings-2"
            />
          </UDropdownMenu>
        </div>
      </div>

      <UTable
        ref="table"
        v-model:column-filters="columnFilters"
        v-model:column-visibility="columnVisibility"
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

        <div class="flex items-center gap-1.5">
          <UPagination
            :default-page="(table?.tableApi?.getState().pagination.pageIndex || 0) + 1"
            :items-per-page="table?.tableApi?.getState().pagination.pageSize"
            :total="table?.tableApi?.getFilteredRowModel().rows.length"
            @update:page="(p: number) => table?.tableApi?.setPageIndex(p - 1)"
          />
        </div>
      </div>
    </template>
  </UDashboardPanel>
</template>
