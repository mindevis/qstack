import { defineEventHandler } from 'nuxt/server'
import type { Disk } from '~/types'

// Demo data until the Block Storage API exists (ADR-0017).
const disks: Disk[] = [
  { id: 'dsk-01m5hqpzg0d3m00000000000d1', name: 'web-1-boot', type: 'local-ssd', sizeGib: 20, status: 'ready', attachedTo: 'web-1', zone: 'ru-central1-a' },
  { id: 'dsk-01m5hqpzg0d3m00000000000d2', name: 'web-2-boot', type: 'local-ssd', sizeGib: 20, status: 'ready', attachedTo: 'web-2', zone: 'ru-central1-a' },
  { id: 'dsk-01m5hqpzg0d3m00000000000d3', name: 'db-1-data', type: 'local-ssd', sizeGib: 200, status: 'ready', attachedTo: 'db-1', zone: 'ru-central1-a' },
  { id: 'dsk-01m5hqpzg0d3m00000000000d4', name: 'worker-1-boot', type: 'local-ssd', sizeGib: 20, status: 'creating', attachedTo: 'worker-1', zone: 'ru-central1-a' },
  { id: 'dsk-01m5hqpzg0d3m00000000000d5', name: 'worker-2-boot', type: 'local-ssd', sizeGib: 20, status: 'ready', attachedTo: 'worker-2', zone: 'ru-central1-a' },
  { id: 'dsk-01m5hqpzg0d3m00000000000d6', name: 'backup', type: 'local-ssd', sizeGib: 500, status: 'ready', zone: 'ru-central1-a' },
  { id: 'dsk-01m5hqpzg0d3m00000000000d7', name: 'gpu-test-boot', type: 'local-ssd', sizeGib: 50, status: 'error', attachedTo: 'gpu-test', zone: 'ru-central1-a' }
]

export default defineEventHandler(async () => {
  return disks
})
