import { computed, onMounted, onUnmounted, ref } from 'vue'

import { api, type Device, type Node, type SSHKey, type VM } from '@/api'

const POLL_MS = 2000

// Module-level state: every view and the sidebar counts read the same poll.
const nodes = ref<Node[]>([])
const vms = ref<VM[]>([])
const keys = ref<SSHKey[]>([])
const devices = ref<Device[]>([])
const loaded = ref(false)
const loadError = ref<Error | null>(null)
const onlineNodes = computed(() => nodes.value.filter((n) => n.online))

async function refresh() {
  try {
    ;[nodes.value, vms.value, keys.value, devices.value] = await Promise.all([
      api.nodes(),
      api.vms(),
      api.keys(),
      api.devices(),
    ])
    loadError.value = null
  } catch (e) {
    loadError.value = e as Error
  } finally {
    loaded.value = true
  }
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
  return { nodes, vms, keys, devices, onlineNodes, loaded, loadError, refresh }
}
