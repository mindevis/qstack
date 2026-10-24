import { defineEventHandler } from 'nuxt/server'
import type { Member } from '~/types'

// Demo access bindings of the organization until the IAM API exists
// (spec 03 §4, ADR-0017).
const members: Member[] = [{
  name: 'Demo Admin',
  username: 'admin@example.com',
  type: 'user',
  role: 'admin',
  avatar: {}
}, {
  name: 'Dev Engineer',
  username: 'dev@example.com',
  type: 'user',
  role: 'editor',
  avatar: {}
}, {
  name: 'Auditor',
  username: 'audit@example.com',
  type: 'user',
  role: 'viewer',
  avatar: {}
}, {
  name: 'ci-deployer',
  username: 'sa-01m5hqpzg0d3m00000000000001',
  type: 'sa',
  role: 'editor',
  avatar: { icon: 'i-lucide-bot' }
}, {
  name: 'monitoring',
  username: 'sa-01m5hqpzg0d3m00000000000002',
  type: 'sa',
  role: 'viewer',
  avatar: { icon: 'i-lucide-bot' }
}]

export default defineEventHandler(async () => {
  return members
})
