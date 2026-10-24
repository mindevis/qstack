import { defineEventHandler } from 'nuxt/server'
import { sub } from 'date-fns'
import type { Instance } from '~/types'

// Demo data until the Compute API exists (ADR-0017).
const instances: Instance[] = [
  { id: 'ins-01m5hqpzg0d3m00000000000a1', name: 'web-1', zone: 'ru-central1-a', type: 's1.medium', vcpus: 2, memoryGib: 4, status: 'running', privateIp: '10.10.0.11', publicIp: '198.51.100.21', createTime: sub(new Date(), { days: 1, hours: 2 }).toISOString() },
  { id: 'ins-01m5hqpzg0d3m00000000000a2', name: 'web-2', zone: 'ru-central1-a', type: 's1.medium', vcpus: 2, memoryGib: 4, status: 'running', privateIp: '10.10.0.12', publicIp: '198.51.100.22', createTime: sub(new Date(), { days: 1, hours: 2 }).toISOString() },
  { id: 'ins-01m5hqpzg0d3m00000000000a3', name: 'db-1', zone: 'ru-central1-a', type: 's1.large', vcpus: 4, memoryGib: 16, status: 'running', privateIp: '10.10.1.5', createTime: sub(new Date(), { days: 3 }).toISOString() },
  { id: 'ins-01m5hqpzg0d3m00000000000a4', name: 'worker-1', zone: 'ru-central1-a', type: 's1.small', vcpus: 1, memoryGib: 2, status: 'provisioning', privateIp: '10.10.0.31', createTime: sub(new Date(), { minutes: 2 }).toISOString() },
  { id: 'ins-01m5hqpzg0d3m00000000000a5', name: 'worker-2', zone: 'ru-central1-a', type: 's1.small', vcpus: 1, memoryGib: 2, status: 'running', privateIp: '10.10.0.32', createTime: sub(new Date(), { hours: 6 }).toISOString() },
  { id: 'ins-01m5hqpzg0d3m00000000000a6', name: 'batch-old', zone: 'ru-central1-a', type: 's1.small', vcpus: 1, memoryGib: 2, status: 'stopped', privateIp: '10.10.0.40', createTime: sub(new Date(), { days: 9 }).toISOString() },
  { id: 'ins-01m5hqpzg0d3m00000000000a7', name: 'gpu-test', zone: 'ru-central1-a', type: 's1.xlarge', vcpus: 8, memoryGib: 32, status: 'error', privateIp: '10.10.0.50', createTime: sub(new Date(), { hours: 3 }).toISOString() }
]

export default defineEventHandler(async () => {
  return instances
})
