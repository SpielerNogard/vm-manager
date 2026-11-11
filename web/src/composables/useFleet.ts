import { computed, onMounted, onUnmounted, ref } from 'vue'

import { api, type Device, type Network, type Node, type Settings, type SSHKey, type VM } from '@/api'

const POLL_MS = 2000

// Module-level state: every view and the sidebar counts read the same poll.
const nodes = ref<Node[]>([])
const vms = ref<VM[]>([])
const keys = ref<SSHKey[]>([])
const devices = ref<Device[]>([])
const networks = ref<Network[]>([])
const settings = ref<Settings | null>(null)
const loaded = ref(false)
const loadError = ref<Error | null>(null)
const onlineNodes = computed(() => nodes.value.filter((n) => n.online))

async function refresh() {
  try {
    ;[nodes.value, vms.value, keys.value, devices.value, networks.value, settings.value] = await Promise.all([
      api.nodes(),
      api.vms(),
      api.keys(),
      api.devices(),
      api.networks(),
      api.settings(),
    ])
    loadError.value = null
  } catch (e) {
    loadError.value = e as Error
  } finally {
    loaded.value = true
  }
}

/** Settings for the router guard, which runs before any component polls. */
export async function ensureSettings() {
  if (!settings.value) settings.value = await api.settings().catch(() => null)
  return settings.value
}

let subscribers = 0
let timer: ReturnType<typeof setInterval> | undefined

/** Shared hub state; polls while at least one component using it is mounted. */
export function useFleet() {
  onMounted(() => {
    if (subscribers++ === 0) {
      void refresh()
      timer = setInterval(refresh, POLL_MS)
    }
  })
  onUnmounted(() => {
    if (--subscribers === 0) clearInterval(timer)
  })
  return { nodes, vms, keys, devices, networks, settings, onlineNodes, loaded, loadError, refresh }
}
