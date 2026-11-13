<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { api } from '@/api'
import AppIcon from '@/components/AppIcon.vue'
import ChassisBadge from '@/components/ChassisBadge.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import MeterBar from '@/components/MeterBar.vue'
import StateBadge from '@/components/StateBadge.vue'
import VMTable from '@/components/VMTable.vue'
import { useFleet } from '@/composables/useFleet'
import { downloadText, flatten, formatMB } from '@/lib/format'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const { nodes, vms, settings, updates, loaded, refresh } = useFleet()

const node = computed(() => nodes.value.find((n) => n.id === route.params.id))
const nodeVMs = computed(() => vms.value.filter((vm) => vm.nodeId === route.params.id))
const config = computed(() => flatten(node.value?.config ?? {}))
const connectedAt = computed(() => {
  const d = node.value && node.value.state !== 'pending' ? new Date(node.value.connectedAt) : null
  return d && d.getFullYear() > 1 ? d.toLocaleString(locale.value) : ''
})
const formatCPUs = (n: number) => `${n} vCPU`

const DEBOUNCE_MS = 300
const host = ref('')
const nodeJson = ref('')
const installCmd = ref('')
const configError = ref('')
const copyError = ref('')
const copied = ref<'' | 'install' | 'config'>('')
const confirmDelete = ref(false)
const deleteError = ref('')
const confirmUpdate = ref(false)
const updateRequestError = ref('')
const runningVMs = computed(() => nodeVMs.value.filter((vm) => vm.state === 'running').length)

watch(
  () => settings.value?.clients.endpoint,
  (endpoint) => {
    if (!host.value && endpoint) host.value = endpoint
  },
)

let fetchTimer: ReturnType<typeof setTimeout> | undefined
let fetchedHost: string | undefined
onUnmounted(() => clearTimeout(fetchTimer))

async function loadConfig() {
  const id = route.params.id
  if (typeof id !== 'string') return
  const h = host.value
  fetchedHost = h
  const current = () => id === route.params.id && h === host.value
  // Settled separately: a node without a hypervisor has no install command but still has a node.json.
  const [json, cmd] = await Promise.allSettled([
    api.nodeConfig(id, h.trim() || undefined),
    api.installCommand(id, h.trim() || undefined),
  ])
  if (!current()) return
  nodeJson.value = json.status === 'fulfilled' ? json.value : ''
  installCmd.value = cmd.status === 'fulfilled' ? cmd.value : ''
  const failed = [cmd, json].find((r) => r.status === 'rejected')
  configError.value = failed ? (failed.reason as Error).message : ''
}

watch(
  () => route.params.id,
  () => {
    clearTimeout(fetchTimer)
    host.value = settings.value?.clients.endpoint ?? ''
    void loadConfig()
  },
  { immediate: true },
)

watch(host, (h) => {
  clearTimeout(fetchTimer)
  if (h !== fetchedHost) fetchTimer = setTimeout(loadConfig, DEBOUNCE_MS)
})

async function copyText(which: 'install' | 'config') {
  try {
    await navigator.clipboard.writeText(which === 'install' ? installCmd.value : nodeJson.value)
    copied.value = which
    copyError.value = ''
    setTimeout(() => (copied.value = ''), 1500)
  } catch {
    copyError.value = t('node.copyFailed')
  }
}

async function updateNode() {
  confirmUpdate.value = false
  updateRequestError.value = ''
  try {
    await api.updateNode(String(route.params.id))
    await refresh()
  } catch (e) {
    updateRequestError.value = (e as Error).message
  }
}

async function deleteNode() {
  confirmDelete.value = false
  deleteError.value = ''
  try {
    await api.deleteNode(String(route.params.id))
    await refresh()
    await router.push({ name: 'nodes' })
  } catch (e) {
    deleteError.value = (e as Error).message
  }
}
</script>

<template>
  <div>
    <nav class="text-base-content/65 mb-2 text-sm">
      <RouterLink :to="{ name: 'nodes' }" class="hover:underline">{{ t('nav.nodes.label') }}</RouterLink>
      <span aria-hidden="true"> / </span>
    </nav>

    <div v-if="!loaded" class="flex justify-center py-16"><span class="loading loading-spinner"></span></div>

    <div v-else-if="!node" class="border-base-300 rounded-box border border-dashed px-6 py-12 text-center">
      <p class="font-medium">{{ t('node.notFound', { id: route.params.id }) }}</p>
    </div>

    <template v-else>
      <div v-if="node.error" role="alert" class="alert alert-warning alert-soft mb-4 text-sm">{{ t('node.providerMismatch', { error: node.error }) }}</div>
      <header class="mb-6">
        <div class="flex flex-wrap items-center gap-3">
          <h1 class="text-2xl font-semibold tracking-tight">{{ node.id }}</h1>
          <StateBadge :label="node.state === 'pending' ? t('nodes.pendingLabel') : t(`state.${node.state}`)" :tone="node.state === 'online' ? 'good' : 'quiet'" />
          <span v-if="node.provider" class="badge badge-sm badge-outline font-token">{{ node.provider }}</span>
        </div>
        <p class="text-base-content/65 mt-1 text-sm">
          <span class="font-token">{{ node.hostname }}</span>
          <template v-if="connectedAt"> · {{ t('node.connectedSince', { time: connectedAt }) }}</template>
        </p>
        <div v-if="node.state !== 'pending'" class="mt-3 flex flex-wrap items-center gap-3 text-sm">
          <span>
            {{ t('node.version') }} <span class="font-token">{{ node.version || t('nodes.versionUnknown') }}</span>
            <template v-if="updates"> · {{ t('node.hubVersion') }} <span class="font-token">{{ updates.version }}</span></template>
          </span>
          <button
            v-if="node.updateAvailable && node.updatable && node.state === 'online'"
            type="button"
            class="btn btn-sm btn-primary"
            :disabled="node.updating"
            @click="confirmUpdate = true"
          >
            <span v-if="node.updating" class="loading loading-spinner loading-xs"></span>
            {{ node.updating ? t('node.updating') : t('node.update') }}
          </button>
          <span v-else-if="node.updateAvailable && !node.updatable" class="text-base-content/60">{{ t('node.updateByHand') }}</span>
        </div>
        <div v-if="updateRequestError" role="alert" class="alert alert-error alert-soft mt-3 text-sm">{{ updateRequestError }}</div>
        <div v-if="node.updateError" role="alert" class="alert alert-error alert-soft mt-3 flex-col items-start text-sm">
          <span>{{ t('node.updateFailed') }}</span>
          <pre class="font-token w-full text-xs break-all whitespace-pre-wrap">{{ node.updateError }}</pre>
        </div>
      </header>

      <div class="mb-6 grid gap-4 lg:grid-cols-2">
        <section class="border-base-300 rounded-box space-y-4 border p-4">
          <h2 class="font-semibold">{{ t('node.capacity') }}</h2>
          <p v-if="node.state === 'pending'" class="text-base-content/60 text-sm">—</p>
          <template v-else>
            <MeterBar :label="t('node.cpu')" :used="node.cpus - node.freeCpus" :total="node.cpus" :format="formatCPUs" />
            <MeterBar :label="t('node.memory')" :used="node.memoryMB - node.freeMemoryMB" :total="node.memoryMB" :format="formatMB" />
            <p class="text-base-content/60 text-xs">{{ t('node.capacityHint') }}</p>
          </template>
        </section>

        <section class="border-base-300 rounded-box border p-4">
          <h2 class="mb-3 font-semibold">{{ t('node.machine') }}</h2>
          <dl v-if="node.machine" class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-sm">
            <dt class="text-base-content/60">{{ t('node.os') }}</dt>
            <dd>{{ node.machine.os === 'darwin' ? 'macOS' : node.machine.os }} {{ node.machine.osVersion }}</dd>
            <dt class="text-base-content/60">{{ t('node.arch') }}</dt>
            <dd class="font-token">{{ node.machine.arch }}</dd>
            <template v-if="node.machine.cpuModel">
              <dt class="text-base-content/60">{{ t('node.cpuModel') }}</dt>
              <dd>{{ node.machine.cpuModel }}</dd>
            </template>
            <template v-if="node.chassis">
              <dt class="text-base-content/60">{{ t('node.chassis') }}</dt>
              <dd class="flex flex-wrap items-center gap-2">
                <span class="font-token">{{ node.chassis.ip }}</span>
                <ChassisBadge :chassis="node.chassis" />
              </dd>
            </template>
            <dt class="text-base-content/60">{{ t('node.hardware') }}</dt>
            <dd class="tabular-nums">
              {{ node.machine.cpus }} vCPU<template v-if="node.machine.memoryMB"> · {{ formatMB(node.machine.memoryMB) }}</template>
            </dd>
          </dl>
          <p v-else class="text-base-content/60 text-sm">{{ t('node.notReported') }}</p>
        </section>
      </div>

      <section class="border-base-300 rounded-box mb-6 space-y-3 border p-4">
        <h2 class="font-semibold">{{ t('node.install') }}</h2>
        <p class="text-base-content/60 text-xs">{{ t('node.installHint') }}</p>
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('node.hubAddress') }}</span>
          <input v-model="host" class="input font-token w-full max-w-md" />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('node.hubAddressHint') }}</span>
        </label>
        <div v-if="configError" role="alert" class="alert alert-error alert-soft text-sm">{{ configError }}</div>
        <div v-if="copyError" role="alert" class="alert alert-error alert-soft text-sm">{{ copyError }}</div>
        <pre class="bg-base-200 rounded-box font-token p-3 text-xs break-all whitespace-pre-wrap">{{ installCmd }}</pre>
        <button type="button" class="btn btn-sm" :disabled="!installCmd" @click="copyText('install')">
          <AppIcon :name="copied === 'install' ? 'check' : 'copy'" />
          {{ copied === 'install' ? t('vms.copied') : t('vms.copy') }}
        </button>
        <details>
          <summary class="cursor-pointer text-sm font-medium">{{ t('node.manualConfig') }}</summary>
          <div class="mt-3 space-y-3">
            <pre class="bg-base-200 rounded-box font-token overflow-x-auto p-3 text-xs">{{ nodeJson }}</pre>
            <div class="flex flex-wrap gap-2">
              <button type="button" class="btn btn-sm" :disabled="!nodeJson" @click="copyText('config')">
                <AppIcon :name="copied === 'config' ? 'check' : 'copy'" />
                {{ copied === 'config' ? t('vms.copied') : t('vms.copy') }}
              </button>
              <button type="button" class="btn btn-sm" :disabled="!nodeJson" @click="downloadText('node.json', nodeJson)">
                {{ t('node.download') }}
              </button>
            </div>
            <p class="text-base-content/60 text-xs">{{ t('node.saveHint') }}</p>
          </div>
        </details>
      </section>

      <section class="border-base-300 rounded-box mb-6 border p-4">
        <h2 class="font-semibold">{{ t('node.config') }}</h2>
        <p class="text-base-content/60 mb-3 text-xs">{{ t('node.configHint') }}</p>
        <table v-if="config.length" class="table-sm table">
          <tbody>
            <tr v-for="[key, value] in config" :key="key">
              <td class="font-token text-base-content/65 w-0 whitespace-nowrap">{{ key }}</td>
              <td class="font-token break-all">{{ value }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="text-base-content/60 text-sm">{{ t('node.notReported') }}</p>
      </section>

      <h2 class="mb-3 font-semibold">{{ t('node.vms') }}</h2>
      <DataState :loading="false" :empty="!nodeVMs.length" :empty-title="t('node.noVMs')">
        <VMTable :vms="nodeVMs" />
      </DataState>

      <div class="mt-8">
        <div v-if="deleteError" role="alert" class="alert alert-error alert-soft mb-3 text-sm">{{ deleteError }}</div>
        <span :class="nodeVMs.length ? 'tooltip' : ''" :data-tip="t('node.deleteBlocked')">
          <button
            type="button"
            class="btn btn-sm btn-error btn-outline"
            :disabled="nodeVMs.length > 0"
            :aria-describedby="nodeVMs.length ? 'node-delete-hint' : undefined"
            @click="confirmDelete = true"
          >
            {{ t('node.delete') }}
          </button>
          <span v-if="nodeVMs.length" id="node-delete-hint" class="sr-only">{{ t('node.deleteBlocked') }}</span>
        </span>
      </div>

      <ConfirmDialog
        :open="confirmDelete"
        :title="t('node.deleteTitle', { name: node.id })"
        :body="node.state === 'online' ? t('node.deleteBodyOnline') : t('node.deleteBody')"
        :confirm-label="t('node.deleteConfirm')"
        @confirm="deleteNode"
        @cancel="confirmDelete = false"
      />
      <ConfirmDialog
        :open="confirmUpdate"
        :title="t('node.updateTitle', { name: node.id, version: updates?.version ?? '' })"
        :body="runningVMs ? t('node.updateBody', { count: runningVMs }, runningVMs) : t('node.updateBodyIdle')"
        :confirm-label="t('node.update')"
        @confirm="updateNode"
        @cancel="confirmUpdate = false"
      />
    </template>
  </div>
</template>
