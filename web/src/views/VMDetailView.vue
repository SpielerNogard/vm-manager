<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { api } from '@/api'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import NetworkRows from '@/components/NetworkRows.vue'
import StateBadge from '@/components/StateBadge.vue'
import { useFleet } from '@/composables/useFleet'
import { formatBytes, formatMB, VM_TONE } from '@/lib/format'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { vms, keys, networks, disks: storedDisks, loaded, refresh } = useFleet()

const vm = computed(() => vms.value.find((v) => v.id === route.params.id))
const host = computed(() => vm.value && `${vm.value.name}.mgmt.internal`)
const sshCommand = computed(() => `ssh ubuntu@${host.value}`)
const rootDisks = computed(() =>
  (vm.value?.disks ?? [])
    .filter((d) => d.name === 'root')
    .map((d) => ({
      key: d.device,
      name: t('vm.rootDisk'),
      device: `/dev/${d.device}`,
      sizeBytes: d.sizeBytes,
      allocatedBytes: d.allocatedBytes,
    })),
)
const attachmentRows = computed(() =>
  (vm.value?.attachments ?? []).map((a) => {
    const reported = (vm.value?.disks ?? []).find((d) => d.name === a.diskId)
    return {
      diskId: a.diskId,
      name: storedDisks.value.find((d) => d.id === a.diskId)?.name ?? a.diskId,
      mountPath: a.mountPath,
      sizeGB: a.sizeGB,
      device: reported?.device ? `/dev/${reported.device}` : '—',
      error: reported?.error,
    }
  }),
)
const attachableDisks = computed(() => storedDisks.value.filter((d) => !d.attachment && d.state !== 'releasing' && d.state !== 'missing'))
const networkName = (id: string) => networks.value.find((n) => n.id === id)?.name ?? id
const attachable = computed(() => networks.value.filter((n) => !n.builtin))
const vmNics = computed(() => (vm.value?.nics ?? []).filter((nic) => nic.network !== 'mgmt'))
const sshKeys = computed(() =>
  (vm.value?.sshPublicKeys ?? []).map((pk) => {
    const known = keys.value.find((k) => k.publicKey.trim() === pk.trim())
    return known ? { label: known.name, detail: known.fingerprint } : { label: shorten(pk), detail: '' }
  }),
)

function shorten(pk: string) {
  const [type, blob = ''] = pk.split(' ')
  return `${type} ${blob.slice(0, 12)}…${blob.slice(-8)}`
}

const powerError = ref('')
const confirmStop = ref(false)

async function power(action: 'startVM' | 'shutdownVM' | 'stopVM') {
  confirmStop.value = false
  if (!vm.value) return
  powerError.value = ''
  try {
    await api[action](vm.value.id)
    await refresh()
  } catch (e) {
    powerError.value = (e as Error).message
  }
}

const editingNetworks = ref(false)
const selectedNetworks = ref<string[]>([])
const confirmRestart = ref(false)
const networkError = ref('')
const savingNetworks = ref(false)

function editNetworks() {
  selectedNetworks.value = vmNics.value.map((nic) => nic.network)
  networkError.value = ''
  editingNetworks.value = true
}

function saveNetworks() {
  if (vm.value?.power === 'running') confirmRestart.value = true
  else void applyNetworks(false)
}

async function applyNetworks(restartAllowed: boolean) {
  confirmRestart.value = false
  if (!vm.value) return
  networkError.value = ''
  savingNetworks.value = true
  try {
    await api.setVMNetworks(vm.value.id, selectedNetworks.value, restartAllowed)
    editingNetworks.value = false
    await refresh()
  } catch (e) {
    networkError.value = (e as Error).message
  } finally {
    savingNetworks.value = false
  }
}

const attachDiskId = ref('')
const attachMountPath = ref('')
const diskError = ref('')
const savingDisks = ref(false)
const pendingDisks = ref<{ diskId: string; mountPath: string }[] | null>(null)
const pendingDetach = ref<string | null>(null)

const currentDisks = () => (vm.value?.attachments ?? []).map((a) => ({ diskId: a.diskId, mountPath: a.mountPath }))

function changeDisks(disks: { diskId: string; mountPath: string }[]) {
  if (vm.value?.power === 'running') pendingDisks.value = disks
  else void applyDisks(disks, false)
}

function attachDisk() {
  changeDisks([...currentDisks(), { diskId: attachDiskId.value, mountPath: attachMountPath.value }])
}

function detachDisk() {
  const id = pendingDetach.value
  pendingDetach.value = null
  if (id) changeDisks(currentDisks().filter((d) => d.diskId !== id))
}

async function applyDisks(disks: { diskId: string; mountPath: string }[], restartAllowed: boolean) {
  pendingDisks.value = null
  if (!vm.value) return
  diskError.value = ''
  savingDisks.value = true
  try {
    await api.setVMDisks(vm.value.id, disks, restartAllowed)
    attachDiskId.value = ''
    attachMountPath.value = ''
    await refresh()
  } catch (e) {
    diskError.value = (e as Error).message
  } finally {
    savingDisks.value = false
  }
}

const copied = ref(false)
async function copy() {
  await navigator.clipboard?.writeText(sshCommand.value)
  copied.value = true
  setTimeout(() => (copied.value = false), 1600)
}
</script>

<template>
  <div>
    <nav class="text-base-content/65 mb-2 text-sm">
      <RouterLink :to="{ name: 'vms' }" class="hover:underline">{{ t('nav.vms.label') }}</RouterLink>
      <span aria-hidden="true"> / </span>
    </nav>

    <div v-if="!loaded" class="flex justify-center py-16"><span class="loading loading-spinner"></span></div>

    <div v-else-if="!vm" class="border-base-300 rounded-box border border-dashed px-6 py-12 text-center">
      <p class="font-medium">{{ t('vm.notFound', { id: route.params.id }) }}</p>
    </div>

    <template v-else>
      <header class="mb-6">
        <div class="flex flex-wrap items-center gap-3">
          <h1 class="text-2xl font-semibold tracking-tight">{{ vm.name }}</h1>
          <StateBadge :label="t(`state.${vm.state}`)" :tone="VM_TONE[vm.state] ?? 'quiet'" :reason="vm.error || (vm.state === 'pending' ? t('vms.waitingForNode') : undefined)" />
          <span class="badge badge-sm badge-outline font-token">{{ vm.provider }}</span>
          <div class="ml-auto flex gap-2">
            <button v-if="vm.power === 'stopped' || vm.state === 'error'" type="button" class="btn btn-sm" @click="power('startVM')">
              {{ t('vms.start') }}
            </button>
            <template v-if="vm.power === 'running'">
              <button type="button" class="btn btn-sm" @click="power('shutdownVM')">{{ t('vms.shutdown') }}</button>
              <button type="button" class="btn btn-sm" @click="confirmStop = true">{{ t('vms.stop') }}</button>
            </template>
          </div>
        </div>
        <p class="text-base-content/65 mt-1 text-sm">
          <span class="font-token">{{ vm.id }}</span> ·
          {{ t('vm.onNode') }}
          <RouterLink :to="{ name: 'node', params: { id: vm.nodeId } }" class="hover:underline">{{ vm.nodeId }}</RouterLink>
        </p>
        <div v-if="vm.error" role="alert" class="alert alert-error alert-soft mt-3 text-sm">{{ vm.error }}</div>
        <div v-if="powerError" role="alert" class="alert alert-error alert-soft mt-3 text-sm">{{ powerError }}</div>
      </header>

      <section class="border-base-300 rounded-box mb-6 border p-4">
        <h2 class="mb-3 font-semibold">{{ t('vm.access') }}</h2>
        <dl class="grid grid-cols-[auto_1fr] items-center gap-x-6 gap-y-1.5 text-sm">
          <dt class="text-base-content/60">{{ t('vms.mgmt') }}</dt>
          <dd class="font-token">{{ host }}</dd>
          <dt class="text-base-content/60">SSH</dt>
          <dd class="flex items-center gap-1">
            <code class="font-token">{{ sshCommand }}</code>
            <button type="button" class="btn btn-ghost btn-xs" :title="t(copied ? 'vms.copied' : 'vms.copy')" @click="copy">
              <AppIcon :name="copied ? 'check' : 'copy'" />
              <span class="sr-only">{{ t('vms.copySsh') }}</span>
            </button>
          </dd>
        </dl>
      </section>

      <section class="border-base-300 rounded-box mb-6 border p-4">
        <div class="flex items-start justify-between gap-3">
          <div>
            <h2 class="font-semibold">{{ t('vm.networks') }}</h2>
            <p class="text-base-content/60 mb-3 text-xs">{{ t('vm.networksHint') }}</p>
          </div>
          <button v-if="!editingNetworks" type="button" class="btn btn-sm" @click="editNetworks">{{ t('vm.editNetworks') }}</button>
        </div>

        <form v-if="editingNetworks" class="space-y-2" @submit.prevent="saveNetworks">
          <NetworkRows v-model="selectedNetworks" :networks="attachable" @manage="router.push({ name: 'networks' })" />
          <div v-if="networkError" role="alert" class="alert alert-error alert-soft text-sm">{{ networkError }}</div>
          <div class="flex justify-end gap-2">
            <button type="button" class="btn btn-sm btn-ghost" @click="editingNetworks = false">{{ t('common.cancel') }}</button>
            <button type="submit" class="btn btn-sm btn-primary" :disabled="savingNetworks">
              <span v-if="savingNetworks" class="loading loading-spinner loading-xs"></span>
              {{ t('vm.save') }}
            </button>
          </div>
        </form>

        <template v-else>
          <div v-if="networkError" role="alert" class="alert alert-error alert-soft mb-3 text-sm">{{ networkError }}</div>
          <p v-if="!vmNics.length" class="text-base-content/60 text-sm">{{ t('vm.noNetworks') }}</p>
          <table v-else class="table-sm table">
            <thead>
              <tr>
                <th>{{ t('common.name') }}</th>
                <th>{{ t('vms.ip') }}</th>
                <th>{{ t('vm.dnsName') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="nic in vmNics" :key="nic.port">
                <td class="font-medium">{{ networkName(nic.network) }}</td>
                <td class="font-token">{{ nic.ip }}</td>
                <td class="font-token">{{ vm.name }}.{{ networkName(nic.network) }}.internal</td>
              </tr>
            </tbody>
          </table>
        </template>
      </section>

      <section class="border-base-300 rounded-box mb-6 border p-4">
        <h2 class="mb-3 font-semibold">{{ t('vm.disks') }}</h2>
        <table class="table-sm table">
          <thead>
            <tr>
              <th>{{ t('vm.disk') }}</th>
              <th>{{ t('vm.device') }}</th>
              <th class="text-right">{{ t('vm.size') }}</th>
              <th class="text-right">{{ t('vm.allocated') }}</th>
              <th>{{ t('vm.filesystem') }}</th>
              <th>{{ t('vm.mountPath') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!rootDisks.length">
              <td colspan="6" class="text-base-content/60">{{ t('vm.notReported') }}</td>
            </tr>
            <tr v-for="d in rootDisks" :key="d.key">
              <td class="font-medium">{{ d.name }}</td>
              <td class="font-token">{{ d.device }}</td>
              <td class="text-right tabular-nums">{{ formatBytes(d.sizeBytes) }}</td>
              <td class="text-right tabular-nums">{{ formatBytes(d.allocatedBytes) }}</td>
              <td class="font-token">ext4</td>
              <td class="font-token">/</td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="border-base-300 rounded-box mb-6 border p-4">
        <h2 class="font-semibold">{{ t('vm.attachedDisks') }}</h2>
        <p class="text-base-content/60 mb-3 text-xs">{{ t('vm.attachedDisksHint') }}</p>
        <div v-if="diskError" role="alert" class="alert alert-error alert-soft mb-3 text-sm">{{ diskError }}</div>
        <p v-if="!attachmentRows.length" class="text-base-content/60 mb-3 text-sm">{{ t('vm.noAttachedDisks') }}</p>
        <table v-else class="table-sm mb-3 table">
          <thead>
            <tr>
              <th>{{ t('vm.disk') }}</th>
              <th>{{ t('vm.mountPath') }}</th>
              <th>{{ t('vm.device') }}</th>
              <th class="text-right">{{ t('vm.size') }}</th>
              <th class="w-0"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in attachmentRows" :key="a.diskId">
              <td class="font-medium">{{ a.name }}</td>
              <td class="font-token">{{ a.mountPath }}</td>
              <td>
                <span class="font-token">{{ a.device }}</span>
                <div v-if="a.error" class="text-error text-xs">{{ a.error }}</div>
              </td>
              <td class="text-right tabular-nums">{{ a.sizeGB }} GB</td>
              <td>
                <button type="button" class="btn btn-ghost btn-xs text-error" :disabled="savingDisks" @click="pendingDetach = a.diskId">
                  {{ t('vm.detach') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
        <form class="flex flex-wrap items-center gap-2" @submit.prevent="attachDisk">
          <select v-model="attachDiskId" class="select select-sm w-auto" required :aria-label="t('vm.disk')">
            <option value="" disabled>{{ t('vm.chooseDisk') }}</option>
            <option v-for="d in attachableDisks" :key="d.id" :value="d.id">{{ d.name }} ({{ d.sizeGB }} GB)</option>
          </select>
          <input
            v-model="attachMountPath"
            class="input input-sm font-token"
            required
            pattern="(/[A-Za-z0-9._\-]+)+"
            placeholder="/mnt/data"
            :aria-label="t('vm.mountPath')"
          />
          <button type="submit" class="btn btn-sm" :disabled="savingDisks || !attachableDisks.length">
            <span v-if="savingDisks" class="loading loading-spinner loading-xs"></span>
            {{ t('vm.attach') }}
          </button>
        </form>
      </section>

      <div class="grid gap-4 lg:grid-cols-2">
        <section class="border-base-300 rounded-box border p-4">
          <h2 class="mb-3 font-semibold">{{ t('vms.resources') }}</h2>
          <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-sm">
            <dt class="text-base-content/60">{{ t('node.cpu') }}</dt>
            <dd class="tabular-nums">{{ vm.cpus }} vCPU</dd>
            <dt class="text-base-content/60">{{ t('node.memory') }}</dt>
            <dd class="tabular-nums">{{ formatMB(vm.memoryMB) }}</dd>
          </dl>
        </section>

        <section class="border-base-300 rounded-box border p-4">
          <h2 class="mb-3 font-semibold">{{ t('vm.sshKeys') }}</h2>
          <ul v-if="sshKeys.length" class="space-y-1.5 text-sm">
            <li v-for="(k, i) in sshKeys" :key="i">
              <span class="font-medium">{{ k.label }}</span>
              <span v-if="k.detail" class="font-token text-base-content/55 ml-2 text-xs">{{ k.detail }}</span>
            </li>
          </ul>
          <p v-else class="text-base-content/60 text-sm">—</p>
        </section>
      </div>
      <ConfirmDialog
        :open="confirmRestart"
        :title="t('vm.restartTitle', { name: vm.name })"
        :body="t('vm.restartBody')"
        :confirm-label="t('vm.restartConfirm')"
        @confirm="applyNetworks(true)"
        @cancel="confirmRestart = false"
      />
      <ConfirmDialog
        :open="pendingDetach !== null"
        :title="t('vm.detachTitle', { name: attachmentRows.find((a) => a.diskId === pendingDetach)?.name })"
        :body="t('vm.detachBody')"
        :confirm-label="t('vm.detach')"
        @confirm="detachDisk"
        @cancel="pendingDetach = null"
      />
      <ConfirmDialog
        :open="pendingDisks !== null"
        :title="t('vm.restartTitle', { name: vm.name })"
        :body="t('vm.restartDisksBody')"
        :confirm-label="t('vm.restartConfirm')"
        @confirm="pendingDisks && applyDisks(pendingDisks, true)"
        @cancel="pendingDisks = null"
      />
      <ConfirmDialog
        :open="confirmStop"
        :title="t('vms.stopTitle', { name: vm.name })"
        :body="t('vms.stopBody')"
        :confirm-label="t('vms.stop')"
        @confirm="power('stopVM')"
        @cancel="confirmStop = false"
      />
    </template>
  </div>
</template>
