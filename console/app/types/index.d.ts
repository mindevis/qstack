import type { AvatarProps } from '@nuxt/ui'

export interface User {
  id: string
  name: string
  email?: string
  avatar?: AvatarProps
}

// IAM access binding of a subject in the organization (spec 03 §4).
export interface Member {
  name: string
  username: string
  type: 'user' | 'sa'
  role: 'admin' | 'editor' | 'viewer'
  avatar: AvatarProps
}

export interface Stat {
  title: string
  icon: string
  value: number | string
  variation: number
  to: string
  formatter?: (value: number) => string
}

export interface Notification {
  id: number
  unread?: boolean
  sender: User
  body: string
  date: string
}

export type Period = 'daily' | 'weekly' | 'monthly'

export interface Range {
  start: Date
  end: Date
}

// qStack resources (demo data until the public API exists, ADR-0017).

export type InstanceStatus = 'running' | 'stopped' | 'provisioning' | 'error'

export interface Instance {
  id: string
  name: string
  zone: string
  type: string
  vcpus: number
  memoryGib: number
  status: InstanceStatus
  privateIp: string
  publicIp?: string
  createTime: string
}

export interface Subnet {
  id: string
  name: string
  zone: string
  cidr: string
}

export interface Network {
  id: string
  name: string
  subnets: Subnet[]
  createTime: string
}

export type DiskStatus = 'ready' | 'creating' | 'error'

export interface Disk {
  id: string
  name: string
  type: 'local-ssd' | 'network-ssd' | 'network-hdd'
  sizeGib: number
  status: DiskStatus
  attachedTo?: string
  zone: string
}

export interface Operation {
  id: string
  description: string
  resourceId: string
  createdBy: string
  createTime: string
  done: boolean
  error?: string
}

export interface ClusterNode {
  id: string
  name: string
  zone: string
  status: 'ready' | 'unreachable' | 'maintenance'
  version: string
  vcpus: { used: number, total: number }
  memoryGib: { used: number, total: number }
  instances: number
  lastHeartbeat: string
}
