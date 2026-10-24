<script setup lang="ts">
import type { TableColumn } from '@nuxt/ui'
import { upperFirst } from 'scule'
import { getPaginationRowModel } from '@tanstack/table-core'
import type { Row } from '@tanstack/table-core'
import type { Instance } from '~/types'

const UButton = resolveComponent('UButton')
const UBadge = resolveComponent('UBadge')
const UDropdownMenu = resolveComponent('UDropdownMenu')
const UCheckbox = resolveComponent('UCheckbox')

const toast = useToast()
const { t } = useI18n()
const { timeAgo } = useFormat()
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
    title: t('app.demo'),
    description: t('instances.actionDemo', { action, name: row.original.name }),
    color: 'info'
  })
}

function getRowItems(row: Row<Instance>) {
  return [
    {
      type: 'label',
      label: t('common.actions')
    },
    {
      label: t('instances.copyId'),
      icon: 'i-lucide-copy',
      onSelect() {
        navigator.clipboard.writeText(row.original.id)
        toast.add({
          title: t('common.copied'),
          description: t('instances.copiedId')
        })
      }
    },
    {
      type: 'separator'
    },
    {
      label: t('instances.start'),
      icon: 'i-lucide-play',
      disabled: row.original.status !== 'stopped',
      onSelect: () => demo(t('instances.start'), row)
    },
    {
      label: t('instances.stop'),
      icon: 'i-lucide-square',
      disabled: row.original.status !== 'running',
      onSelect: () => demo(t('instances.stop'), row)
    },
    {
      label: t('instances.serialConsole'),
      icon: 'i-lucide-terminal',
      onSelect: () => demo(t('instances.serialConsole'), row)
    },
    {
      type: 'separator'
    },
    {
      label: t('instances.deleteOne'),
      icon: 'i-lucide-trash',
      color: 'error',
      onSelect: () => demo(t('common.delete'), row)
    }
  ]
}

const statusColor = {
  running: 'success' as const,
  provisioning: 'info' as const,
  stopped: 'neutral' as const,
  error: 'error' as const
}

const columns = computed<TableColumn<Instance>[]>(() => [
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
        label: t('common.name'),
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
    header: t('common.status'),
    filterFn: 'equals',
    cell: ({ row }) => {
      return h(UBadge, { variant: 'subtle', color: statusColor[row.original.status] }, () =>
        t(`status.${row.original.status}`)
      )
    }
  },
  {
    accessorKey: 'type',
    header: t('common.type'),
    cell: ({ row }) => `${row.original.type} · ${row.original.vcpus} vCPU · ${row.original.memoryGib} GiB`
  },
  {
    accessorKey: 'zone',
    header: t('common.zone')
  },
  {
    id: 'addresses',
    header: t('common.addresses'),
    cell: ({ row }) => h('div', { class: 'font-mono text-xs' }, [
      h('p', undefined, row.original.privateIp),
      row.original.publicIp ? h('p', { class: 'text-primary' }, row.original.publicIp) : null
    ])
  },
  {
    accessorKey: 'createTime',
    header: t('common.created'),
    cell: ({ row }) => timeAgo(row.original.createTime)
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
])

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
      <UDashboardNavbar :title="t('instances.title')">
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
          :placeholder="t('common.filterNames')"
        />

        <div class="flex flex-wrap items-center gap-1.5">
          <ResourcesDeleteModal :kind="t('instances.kind')" :count="table?.tableApi?.getFilteredSelectedRowModel().rows.length">
            <UButton
              v-if="table?.tableApi?.getFilteredSelectedRowModel().rows.length"
              :label="t('common.delete')"
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
              { label: t('common.all'), value: 'all' },
              { label: t('status.running'), value: 'running' },
              { label: t('status.provisioning'), value: 'provisioning' },
              { label: t('status.stopped'), value: 'stopped' },
              { label: t('status.error'), value: 'error' }
            ]"
            :ui="{ trailingIcon: 'group-data-[state=open]:rotate-180 transition-transform duration-200' }"
            :placeholder="t('common.filterStatus')"
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
              :label="t('common.display')"
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
          {{ t('common.rowsSelected', {
            selected: table?.tableApi?.getFilteredSelectedRowModel().rows.length || 0,
            total: table?.tableApi?.getFilteredRowModel().rows.length || 0
          }) }}
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
