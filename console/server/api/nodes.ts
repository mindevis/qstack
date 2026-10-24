import { defineEventHandler } from 'nuxt/server'
import { sub } from 'date-fns'
import type { ClusterNode } from '~/types'

// Demo data until the cluster admin API exists (spec 15 §4, ADR-0017).
const nodes: ClusterNode[] = [
  { id: 'hst-01m5hqpzg0d3m00000000000f1', name: 'node-1', zone: 'ru-central1-a', status: 'ready', version: 'v2026.10.0', vcpus: { used: 9, total: 32 }, memoryGib: { used: 38, total: 128 }, instances: 4, lastHeartbeat: sub(new Date(), { seconds: 4 }).toISOString() },
  { id: 'hst-01m5hqpzg0d3m00000000000f2', name: 'node-2', zone: 'ru-central1-a', status: 'ready', version: 'v2026.10.0', vcpus: { used: 2, total: 32 }, memoryGib: { used: 6, total: 128 }, instances: 2, lastHeartbeat: sub(new Date(), { seconds: 7 }).toISOString() },
  { id: 'hst-01m5hqpzg0d3m00000000000f3', name: 'node-3', zone: 'ru-central1-a', status: 'maintenance', version: 'v2026.09.2', vcpus: { used: 0, total: 16 }, memoryGib: { used: 0, total: 64 }, instances: 0, lastHeartbeat: sub(new Date(), { seconds: 9 }).toISOString() }
]

export default defineEventHandler(async () => {
  return nodes
})
