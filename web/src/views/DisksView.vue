<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'

import { api, type StoredDisk } from '@/api'
import AddDiskDialog from '@/components/AddDiskDialog.vue'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StateBadge from '@/components/StateBadge.vue'
import { useFleet } from '@/composables/useFleet'
import { formatBytes, type Tone } from '@/lib/format'

const { t } = useI18n()
const { disks, vms, storage, loaded, refresh } = useFleet()

const DISK_TONE: Record<StoredDisk['state'], Tone> = { creating: 'quiet', ready: 'good', releasing: 'warning' }

const addOpen = ref(false)
const error = ref('')
const notice = ref('')

const pendingDelete = ref<StoredDisk | null>(null)
const pendingRelease = ref<StoredDisk | null>(null)

const copySource = ref<StoredDisk | null>(null)
const copyName = ref('')
const growTarget = ref<StoredDisk | null>(null)
const growSize = ref(0)
const confirmGrowRestart = ref(false)
const dialogError = ref('')
const busy = ref(false)

const attachedRunning = (d: StoredDisk) =>
  vms.value.find((v) => v.id === d.attachment?.vmId)?.power === 'running'
const orphanCount = computed(() => storage.value.orphans.length)

async function run(action: () => Promise<void>) {
  error.value = ''
  try {
    await action()
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  }
}

function deleteDisk() {
  const d = pendingDelete.value
  pendingDelete.value = null
  if (d) void run(() => api.deleteDisk(d.id))
}

function forceRelease() {
  const d = pendingRelease.value
  pendingRelease.value = null
  if (d) void run(() => api.forceRelease(d.id))
}

function openCopy(d: StoredDisk) {
  copySource.value = d
  copyName.value = `${d.name}-copy`.slice(0, 63)
  dialogError.value = ''
}

async function submitCopy() {
  const d = copySource.value
  if (!d) return
  dialogError.value = ''
  busy.value = true
  try {
    const res = await api.copyDisk(d.id, copyName.value)
    notice.value = res.warning ?? ''
    copySource.value = null
    await refresh()
  } catch (e) {
    dialogError.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

function openGrow(d: StoredDisk) {
  growTarget.value = d
  growSize.value = d.sizeGB + 1
  dialogError.value = ''
}

function submitGrow() {
  if (growTarget.value && attachedRunning(growTarget.value)) confirmGrowRestart.value = true
  else void applyGrow(false)
}

async function applyGrow(restartAllowed: boolean) {
  confirmGrowRestart.value = false
  const d = growTarget.value
  if (!d) return
  dialogError.value = ''
  busy.value = true
  try {
    await api.growDisk(d.id, growSize.value, restartAllowed)
    growTarget.value = null
    await refresh()
  } catch (e) {
    dialogError.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader section="disks">
      <template #actions>
        <StateBadge
          :label="t(storage.online ? 'disks.storageOnline' : 'disks.storageOffline')"
          :tone="storage.online ? 'good' : 'danger'"
          :reason="storage.endpoint || undefined"
        />
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('disks.add') }}
        </button>
      </template>
    </PageHeader>

    <div v-if="error" role="alert" class="alert alert-error alert-soft mb-4 text-sm">{{ error }}</div>
    <div v-if="notice" role="alert" class="alert alert-warning alert-soft mb-4 text-sm">{{ notice }}</div>

    <DataState :loading="!loaded" :empty="!disks.length" :empty-title="t('disks.emptyTitle')">
      <template #empty-body>{{ t('disks.emptyBody') }}</template>
      <template #empty-action>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('disks.add') }}
        </button>
      </template>

      <table class="table-sm table">
        <thead>
          <tr>
            <th>{{ t('common.name') }}</th>
            <th class="text-right">{{ t('disks.size') }}</th>
            <th class="text-right">{{ t('disks.allocated') }}</th>
            <th>{{ t('common.status') }}</th>
            <th>{{ t('disks.attachedTo') }}</th>
            <th>{{ t('disks.created') }}</th>
            <th class="w-0"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in disks" :key="d.id">
            <td class="font-medium">
              {{ d.name }}
              <div class="font-token text-base-content/55 text-[0.7rem]">{{ d.id }}</div>
            </td>
            <td class="text-right tabular-nums">{{ d.sizeGB }} GB</td>
            <td class="text-right tabular-nums">{{ formatBytes(d.allocatedBytes) }}</td>
            <td>
              <StateBadge
                :label="t(`disks.state.${d.state}`)"
                :tone="DISK_TONE[d.state]"
                :reason="d.releasingOn ? t('disks.releasingOn', { node: d.releasingOn }) : undefined"
              />
              <div v-if="d.releasingOn" class="text-base-content/60 text-xs">{{ t('disks.releasingOn', { node: d.releasingOn }) }}</div>
            </td>
            <td class="text-sm">
              <template v-if="d.attachment">
                <RouterLink :to="{ name: 'vm', params: { id: d.attachment.vmId } }" class="hover:underline">{{ d.attachment.vmName }}</RouterLink>
                <div class="font-token text-base-content/55 text-xs">{{ d.attachment.mountPath }}</div>
              </template>
              <span v-else class="text-base-content/60">—</span>
            </td>
            <td class="text-sm">{{ new Date(d.createdAt).toLocaleString() }}</td>
            <td>
              <div class="flex gap-1">
                <button type="button" class="btn btn-ghost btn-xs" @click="openCopy(d)">{{ t('disks.copy') }}</button>
                <button type="button" class="btn btn-ghost btn-xs" :disabled="d.state === 'releasing'" @click="openGrow(d)">
                  {{ t('disks.grow') }}
                </button>
                <button
                  v-if="d.state === 'releasing'"
                  type="button"
                  class="btn btn-ghost btn-xs text-error"
                  @click="pendingRelease = d"
                >
                  {{ t('disks.forceRelease') }}
                </button>
                <span :title="d.attachment ? t('disks.inUse') : undefined">
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs text-error"
                    :disabled="!!d.attachment || d.state === 'releasing'"
                    @click="pendingDelete = d"
                  >
                    {{ t('common.delete') }}
                  </button>
                </span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </DataState>

    <section v-if="orphanCount" class="border-base-300 rounded-box mt-6 border p-4">
      <h2 class="font-semibold">{{ t('disks.orphans') }}</h2>
      <p class="text-base-content/60 mb-3 text-xs">{{ t('disks.orphansHint') }}</p>
      <table class="table-sm table">
        <thead>
          <tr>
            <th>{{ t('common.name') }}</th>
            <th class="text-right">{{ t('disks.size') }}</th>
            <th class="text-right">{{ t('disks.allocated') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="f in storage.orphans" :key="f.name">
            <td class="font-token">{{ f.name }}</td>
            <td class="text-right tabular-nums">{{ formatBytes(f.sizeBytes) }}</td>
            <td class="text-right tabular-nums">{{ formatBytes(f.allocatedBytes) }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <AddDiskDialog v-model:open="addOpen" />

    <dialog class="modal" :open="copySource !== null" @close="copySource = null">
      <div class="modal-box max-w-xl">
        <h3 class="text-lg font-semibold tracking-tight">{{ t('disks.copyTitle', { name: copySource?.name }) }}</h3>
        <form class="mt-4 space-y-4" @submit.prevent="submitCopy">
          <label class="block">
            <span class="mb-1 block text-sm font-medium">{{ t('disks.copyName') }}</span>
            <input
              v-model="copyName"
              class="input w-full"
              required
              maxlength="63"
              pattern="[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?"
            />
          </label>
          <div v-if="copySource?.attachment || copySource?.state === 'releasing'" role="alert" class="alert alert-warning alert-soft text-sm">
            {{ t('disks.copyWarning') }}
          </div>
          <div v-if="dialogError" role="alert" class="alert alert-error alert-soft text-sm">{{ dialogError }}</div>
          <div class="modal-action">
            <button type="button" class="btn btn-sm btn-ghost" @click="copySource = null">{{ t('common.cancel') }}</button>
            <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
              <span v-if="busy" class="loading loading-spinner loading-xs"></span>
              {{ t('disks.copy') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop" @click="copySource = null">{{ t('common.close') }}</button>
    </dialog>

    <dialog class="modal" :open="growTarget !== null" @close="growTarget = null">
      <div class="modal-box max-w-xl">
        <h3 class="text-lg font-semibold tracking-tight">{{ t('disks.growTitle', { name: growTarget?.name }) }}</h3>
        <form class="mt-4 space-y-4" @submit.prevent="submitGrow">
          <label class="block">
            <span class="mb-1 block text-sm font-medium">{{ t('disks.newSizeGB') }}</span>
            <input v-model.number="growSize" class="input w-full" type="number" :min="(growTarget?.sizeGB ?? 0) + 1" required />
            <span class="text-base-content/60 mt-1 block text-xs">{{ t('disks.growHint', { size: growTarget?.sizeGB }) }}</span>
          </label>
          <div v-if="dialogError" role="alert" class="alert alert-error alert-soft text-sm">{{ dialogError }}</div>
          <div class="modal-action">
            <button type="button" class="btn btn-sm btn-ghost" @click="growTarget = null">{{ t('common.cancel') }}</button>
            <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
              <span v-if="busy" class="loading loading-spinner loading-xs"></span>
              {{ t('disks.grow') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop" @click="growTarget = null">{{ t('common.close') }}</button>
    </dialog>

    <ConfirmDialog
      :open="confirmGrowRestart"
      :title="t('disks.growRestartTitle', { name: growTarget?.attachment?.vmName })"
      :body="t('disks.growRestartBody')"
      :confirm-label="t('vm.restartConfirm')"
      @confirm="applyGrow(true)"
      @cancel="confirmGrowRestart = false"
    />
    <ConfirmDialog
      :open="pendingDelete !== null"
      :title="t('disks.deleteTitle', { name: pendingDelete?.name })"
      :body="t('disks.deleteBody')"
      :confirm-label="t('disks.deleteConfirm')"
      @confirm="deleteDisk"
      @cancel="pendingDelete = null"
    />
    <ConfirmDialog
      :open="pendingRelease !== null"
      :title="t('disks.releaseTitle', { name: pendingRelease?.name })"
      :body="t('disks.releaseBody', { node: pendingRelease?.releasingOn })"
      :confirm-label="t('disks.forceRelease')"
      @confirm="forceRelease"
      @cancel="pendingRelease = null"
    />
  </div>
</template>
