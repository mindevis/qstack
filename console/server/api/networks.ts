import { defineEventHandler } from 'nuxt/server'
import { sub } from 'date-fns'
import type { Network } from '~/types'

// Demo data until the VPC API exists (ADR-0017).
const networks: Network[] = [{
  id: 'vpc-01m5hqpzg0d3m00000000000b1',
  name: 'main',
  createTime: sub(new Date(), { days: 30 }).toISOString(),
  subnets: [
    { id: 'sub-01m5hqpzg0d3m00000000000c1', name: 'frontend', zone: 'ru-central1-a', cidr: '10.10.0.0/24' },
    { id: 'sub-01m5hqpzg0d3m00000000000c2', name: 'backend', zone: 'ru-central1-a', cidr: '10.10.1.0/24' }
  ]
}, {
  id: 'vpc-01m5hqpzg0d3m00000000000b2',
  name: 'sandbox',
  createTime: sub(new Date(), { days: 3 }).toISOString(),
  subnets: [
    { id: 'sub-01m5hqpzg0d3m00000000000c3', name: 'default', zone: 'ru-central1-a', cidr: '172.16.0.0/24' }
  ]
}]

export default defineEventHandler(async () => {
  return networks
})
