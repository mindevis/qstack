// Liveness/readiness for Kubernetes probes (qs-console).
import { defineEventHandler } from 'nuxt/server'

export default defineEventHandler(() => ({ status: 'ok' }))
