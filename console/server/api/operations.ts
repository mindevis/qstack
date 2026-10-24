import { defineEventHandler } from 'nuxt/server'
import { sub } from 'date-fns'
import type { Operation } from '~/types'

// Demo data until qs-operations exists (ADR-0017).
const operations: Operation[] = [
  { id: 'op-01m5hqpzg0d3m000000000000e1', description: 'Create instance worker-1', resourceId: 'ins-01m5hqpzg0d3m00000000000a4', createdBy: 'admin@example.com', createTime: sub(new Date(), { minutes: 2 }).toISOString(), done: false },
  { id: 'op-01m5hqpzg0d3m000000000000e2', description: 'Create disk worker-1-boot', resourceId: 'dsk-01m5hqpzg0d3m00000000000d4', createdBy: 'admin@example.com', createTime: sub(new Date(), { minutes: 2 }).toISOString(), done: false },
  { id: 'op-01m5hqpzg0d3m000000000000e3', description: 'Create instance gpu-test', resourceId: 'ins-01m5hqpzg0d3m00000000000a7', createdBy: 'ci-deployer', createTime: sub(new Date(), { hours: 3 }).toISOString(), done: true, error: 'RESOURCE_EXHAUSTED (QS_ZONE_CAPACITY): no host in ru-central1-a can fit s1.xlarge' },
  { id: 'op-01m5hqpzg0d3m000000000000e4', description: 'Stop instance batch-old', resourceId: 'ins-01m5hqpzg0d3m00000000000a6', createdBy: 'dev@example.com', createTime: sub(new Date(), { hours: 5 }).toISOString(), done: true },
  { id: 'op-01m5hqpzg0d3m000000000000e5', description: 'Create instance worker-2', resourceId: 'ins-01m5hqpzg0d3m00000000000a5', createdBy: 'ci-deployer', createTime: sub(new Date(), { hours: 6 }).toISOString(), done: true },
  { id: 'op-01m5hqpzg0d3m000000000000e6', description: 'Create instance batch-2', resourceId: 'ins-01m5hqpzg0d3m00000000000a9', createdBy: 'ci-deployer', createTime: sub(new Date(), { hours: 7 }).toISOString(), done: true, error: 'RESOURCE_EXHAUSTED (QS_QUOTA_EXCEEDED): compute.instances.count limit 8' },
  { id: 'op-01m5hqpzg0d3m000000000000e7', description: 'Create subnet default', resourceId: 'sub-01m5hqpzg0d3m00000000000c3', createdBy: 'dev@example.com', createTime: sub(new Date(), { days: 3 }).toISOString(), done: true },
  { id: 'op-01m5hqpzg0d3m000000000000e8', description: 'Create network sandbox', resourceId: 'vpc-01m5hqpzg0d3m00000000000b2', createdBy: 'dev@example.com', createTime: sub(new Date(), { days: 3 }).toISOString(), done: true }
]

export default defineEventHandler(async () => {
  return operations
})
