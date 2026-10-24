import { defineEventHandler } from 'nuxt/server'
import { sub } from 'date-fns'
import type { Notification } from '~/types'

// Demo notifications about operations until qs-operations exists (ADR-0017).
const notifications: Notification[] = [{
  id: 1,
  unread: true,
  sender: { id: 'usr-01m5hqpzg0d3m0000000000001', name: 'admin@example.com' },
  body: 'started Create instance worker-1',
  date: sub(new Date(), { minutes: 2 }).toISOString()
}, {
  id: 2,
  unread: true,
  sender: { id: 'sa-01m5hqpzg0d3m00000000000001', name: 'ci-deployer', avatar: { icon: 'i-lucide-bot' } },
  body: 'failed to create instance gpu-test: no capacity in ru-central1-a',
  date: sub(new Date(), { hours: 3 }).toISOString()
}, {
  id: 3,
  sender: { id: 'usr-01m5hqpzg0d3m0000000000002', name: 'dev@example.com' },
  body: 'stopped instance batch-old',
  date: sub(new Date(), { hours: 5 }).toISOString()
}, {
  id: 4,
  sender: { id: 'sa-01m5hqpzg0d3m00000000000001', name: 'ci-deployer', avatar: { icon: 'i-lucide-bot' } },
  body: 'hit the quota compute.instances.count',
  date: sub(new Date(), { hours: 7 }).toISOString()
}, {
  id: 5,
  sender: { id: 'usr-01m5hqpzg0d3m0000000000002', name: 'dev@example.com' },
  body: 'created network sandbox',
  date: sub(new Date(), { days: 3 }).toISOString()
}]

export default defineEventHandler(async () => {
  return notifications
})
