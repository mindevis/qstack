// The signed-in user, their organizations and projects. Demo data until
// the OIDC login with qs-iam (T-1.10, T-1.63): the console server (BFF)
// will then read its httpOnly session cookie; tokens never reach the
// browser (spec 10 §4, ADR-0017).
import { defineEventHandler } from 'nuxt/server'

export default defineEventHandler(() => ({
  demo: true,
  region: 'ru-central1',
  user: {
    id: 'usr-01m5hqpzg0d3m0000000000001',
    name: 'Demo Admin',
    email: 'admin@example.com',
    username: 'admin'
  },
  organizations: [{
    id: 'org-01m5hqpzg0d3m0000000000001',
    name: 'test',
    projects: [
      { id: 'prj-01m5hqpzg0d3m0000000000001', name: 'default' },
      { id: 'prj-01m5hqpzg0d3m0000000000002', name: 'staging' }
    ]
  }]
}))
