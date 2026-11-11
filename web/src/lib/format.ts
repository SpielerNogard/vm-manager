import type { VMState } from '@/api'

export const formatMB = (mb: number) => (mb >= 1024 ? `${+(mb / 1024).toFixed(1)} GB` : `${mb} MB`)

// Status colours are only ever used for status, and always next to a label.
export type Tone = 'good' | 'warning' | 'danger' | 'quiet'

export const TONE_DOT: Record<Tone, string> = {
  good: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-error',
  quiet: 'bg-base-content/30',
}

export const VM_TONE: Record<VMState, Tone> = {
  pending: 'quiet',
  starting: 'quiet',
  running: 'good',
  stopped: 'quiet',
  error: 'danger',
  unknown: 'warning',
}

/** Flattens nested config into dotted keys, e.g. { hub: { url } } → ["hub.url", url]. */
export function flatten(value: unknown, prefix = ''): [string, string][] {
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    return Object.entries(value).flatMap(([k, v]) => flatten(v, prefix ? `${prefix}.${k}` : k))
  }
  return [[prefix, typeof value === 'string' ? value : JSON.stringify(value)]]
}

export function downloadText(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
