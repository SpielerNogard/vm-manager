<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import { api, type CreateVMRequest } from '@/api'
import { useFleet } from '@/composables/useFleet'
import { NAME_PATTERN } from '@/lib/names'

import AppIcon from './AppIcon.vue'
import NetworkRows from './NetworkRows.vue'

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const router = useRouter()
const { onlineNodes, keys, networks, refresh } = useFleet()

const form = ref<CreateVMRequest & { networkIds: string[] }>({ nodeId: '', name: '', cpus: 2, memoryMB: 2048, diskGB: 20, dataDisks: [], sshKeyIds: [], networkIds: [] })
const error = ref('')
const busy = ref(false)

// Hub keys are preselected every time; otherwise a single stored key is.
watch(open, (isOpen) => {
  if (!isOpen) return
  error.value = ''
  const f = form.value
  if (!onlineNodes.value.some((n) => n.id === f.nodeId)) f.nodeId = onlineNodes.value[0]?.id ?? ''
  const hubKeys = keys.value.filter((k) => k.hub).map((k) => k.id)
  const only = keys.value.length === 1 ? keys.value[0] : undefined
  f.sshKeyIds = hubKeys.length || !only ? hubKeys : [only.id]
})

const userNetworks = computed(() => networks.value.filter((n) => !n.builtin))

function addDisk() {
  const n = form.value.dataDisks.length
  const name = n ? `data${n + 1}` : 'data'
  form.value.dataDisks.push({ name, mountPath: `/mnt/${name}`, sizeGB: 10 })
}

function goToKeys() {
  open.value = false
  void router.push({ name: 'keys', query: { add: '1' } })
}

function goToNetworks() {
  open.value = false
  void router.push({ name: 'networks' })
}

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.createVM(form.value)
    form.value.name = ''
    form.value.dataDisks = []
    form.value.networkIds = []
    open.value = false
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <dialog class="modal" :open="open" @close="open = false">
    <div class="modal-box max-w-xl">
      <h3 class="text-lg font-semibold tracking-tight">{{ t('create.title') }}</h3>
      <form class="mt-4 grid grid-cols-2 gap-4" @submit.prevent="submit">
        <label class="col-span-2 sm:col-span-1">
          <span class="mb-1 block text-sm font-medium">{{ t('create.node') }}</span>
          <select v-model="form.nodeId" class="select w-full" required>
            <option v-for="n in onlineNodes" :key="n.id" :value="n.id">{{ n.id }} ({{ n.provider }})</option>
          </select>
        </label>
        <label class="col-span-2 sm:col-span-1">
          <span class="mb-1 block text-sm font-medium">{{ t('common.name') }}</span>
          <input
            v-model="form.name"
            class="input w-full"
            required
            :pattern="NAME_PATTERN"
            placeholder="dev-1"
          />
        </label>
        <label>
          <span class="mb-1 block text-sm font-medium">{{ t('create.cpus') }}</span>
          <input v-model.number="form.cpus" class="input w-full" type="number" min="1" required />
        </label>
        <label>
          <span class="mb-1 block text-sm font-medium">{{ t('create.memory') }}</span>
          <input v-model.number="form.memoryMB" class="input w-full" type="number" min="512" step="256" required />
        </label>
        <label>
          <span class="mb-1 block text-sm font-medium">{{ t('create.rootDisk') }}</span>
          <input v-model.number="form.diskGB" class="input w-full" type="number" min="4" required />
        </label>

        <fieldset class="col-span-2">
          <legend class="mb-1 text-sm font-medium">{{ t('create.dataDisks') }}</legend>
          <p class="text-base-content/60 mb-2 text-xs">{{ t('create.dataDisksHint') }}</p>
          <div
            v-if="form.dataDisks.length"
            class="text-base-content/60 mb-1 grid grid-cols-[1fr_1.5fr_5rem_2rem] gap-2 text-xs"
          >
            <span>{{ t('common.name') }}</span><span>{{ t('create.mountPath') }}</span><span>{{ t('create.sizeGB') }}</span><span></span>
          </div>
          <div
            v-for="(d, i) in form.dataDisks"
            :key="i"
            class="mb-2 grid grid-cols-[1fr_1.5fr_5rem_2rem] items-center gap-2"
          >
            <input
              v-model="d.name"
              class="input input-sm w-full"
              required
              maxlength="16"
              pattern="[a-z0-9]([a-z0-9\-]{0,14}[a-z0-9])?"
              placeholder="data"
              :aria-label="t('create.diskName')"
            />
            <input
              v-model="d.mountPath"
              class="input input-sm font-token w-full"
              required
              pattern="(/[A-Za-z0-9._\-]+)+"
              placeholder="/mnt/data"
              :aria-label="t('create.mountPath')"
            />
            <input
              v-model.number="d.sizeGB"
              class="input input-sm w-full"
              type="number"
              min="1"
              required
              :aria-label="t('create.diskSize')"
            />
            <button
              type="button"
              class="btn btn-ghost btn-sm btn-square"
              :title="t('create.removeDisk')"
              @click="form.dataDisks.splice(i, 1)"
            >
              <AppIcon name="trash" />
              <span class="sr-only">{{ t('create.removeDisk') }}</span>
            </button>
          </div>
          <button type="button" class="btn btn-sm btn-ghost" @click="addDisk">
            <AppIcon name="plus" />
            {{ t('create.addDisk') }}
          </button>
        </fieldset>

        <fieldset class="col-span-2">
          <legend class="mb-1 text-sm font-medium">{{ t('create.networks') }}</legend>
          <p class="text-base-content/60 mb-2 text-xs">{{ t('create.networksHint') }}</p>
          <NetworkRows v-model="form.networkIds" :networks="userNetworks" @manage="goToNetworks" />
        </fieldset>

        <fieldset class="col-span-2">
          <legend class="mb-1 text-sm font-medium">{{ t('create.sshKeys') }}</legend>
          <div
            v-if="!keys.length"
            class="border-base-300 rounded-box flex items-center justify-between gap-3 border border-dashed p-3 text-sm"
          >
            <span class="text-base-content/65">{{ t('create.noKeys') }}</span>
            <button type="button" class="btn btn-sm" @click="goToKeys">{{ t('keys.add') }}</button>
          </div>
          <label
            v-for="k in keys"
            :key="k.id"
            class="hover:bg-base-200/50 rounded-field flex cursor-pointer items-center gap-3 px-2 py-1.5"
          >
            <input v-model="form.sshKeyIds" type="checkbox" class="checkbox checkbox-sm" :value="k.id" />
            <span class="min-w-0">
              <span class="block text-sm font-medium">
                {{ k.name }}<span v-if="k.hub" class="badge badge-xs badge-accent ml-2">{{ t('keys.hub') }}</span>
              </span>
              <span class="font-token text-base-content/55 block truncate text-[0.7rem]">{{ k.fingerprint }}</span>
            </span>
          </label>
        </fieldset>

        <div v-if="error" role="alert" class="alert alert-error alert-soft col-span-2 text-sm">{{ error }}</div>
        <div class="modal-action col-span-2 mt-0">
          <button type="button" class="btn btn-sm btn-ghost" @click="open = false">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-sm btn-primary" :disabled="busy || !form.sshKeyIds.length">
            <span v-if="busy" class="loading loading-spinner loading-xs"></span>
            {{ t('create.submit') }}
          </button>
        </div>
      </form>
    </div>
    <button type="button" class="modal-backdrop" @click="open = false">{{ t('common.close') }}</button>
  </dialog>
</template>
