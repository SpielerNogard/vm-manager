<script setup lang="ts">
import QRCode from 'qrcode'
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { api } from '@/api'
import { useFleet } from '@/composables/useFleet'
import { downloadText } from '@/lib/format'

import AppIcon from './AppIcon.vue'

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const { refresh } = useFleet()

const name = ref('')
const error = ref('')
const busy = ref(false)
const created = ref<{ name: string; config: string; qr: string } | null>(null)

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  error.value = ''
  created.value = null
})

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const { device, config } = await api.addDevice(name.value)
    created.value = { name: device.name, config, qr: await QRCode.toDataURL(config, { margin: 1, width: 240 }) }
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
      <h3 class="text-lg font-semibold tracking-tight">{{ t('devices.dialogTitle') }}</h3>

      <template v-if="created">
        <p class="text-base-content/65 mt-2 text-sm">{{ t('devices.createdHint') }}</p>
        <div class="mt-4 flex flex-col items-center gap-4 sm:flex-row sm:items-start">
          <img :src="created.qr" :alt="t('devices.qrAlt')" class="size-48 shrink-0 rounded bg-white" />
          <pre class="font-token bg-base-200 rounded-box max-h-60 w-full overflow-auto p-3 text-xs">{{ created.config }}</pre>
        </div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost" @click="downloadText(`${created.name}.conf`, created.config)">
            <AppIcon name="download" />
            {{ t('devices.download') }}
          </button>
          <button type="button" class="btn btn-sm btn-primary" @click="open = false">{{ t('common.close') }}</button>
        </div>
      </template>

      <form v-else class="mt-4 space-y-4" @submit.prevent="submit">
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('common.name') }}</span>
          <input
            v-model="name"
            class="input w-full"
            required
            maxlength="63"
            pattern="[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?"
            placeholder="macbook"
          />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('devices.nameHint') }}</span>
        </label>
        <div v-if="error" role="alert" class="alert alert-error alert-soft text-sm">{{ error }}</div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost" @click="open = false">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
            <span v-if="busy" class="loading loading-spinner loading-xs"></span>
            {{ t('devices.create') }}
          </button>
        </div>
      </form>
    </div>
    <button type="button" class="modal-backdrop" @click="open = false">{{ t('common.close') }}</button>
  </dialog>
</template>
