<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { api } from '@/api'
import { useFleet } from '@/composables/useFleet'

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const { refresh } = useFleet()

const name = ref('')
const sizeGB = ref(10)
const error = ref('')
const busy = ref(false)

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  sizeGB.value = 10
  error.value = ''
})

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.createDisk(name.value, sizeGB.value)
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
      <h3 class="text-lg font-semibold tracking-tight">{{ t('disks.dialogTitle') }}</h3>
      <form class="mt-4 space-y-4" @submit.prevent="submit">
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('common.name') }}</span>
          <input
            v-model="name"
            class="input w-full"
            required
            maxlength="63"
            pattern="[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?"
            placeholder="data"
          />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('disks.nameHint') }}</span>
        </label>
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('disks.sizeGB') }}</span>
          <input v-model.number="sizeGB" class="input w-full" type="number" min="1" required />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('disks.sizeHint') }}</span>
        </label>
        <div v-if="error" role="alert" class="alert alert-error alert-soft text-sm">{{ error }}</div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost" @click="open = false">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
            <span v-if="busy" class="loading loading-spinner loading-xs"></span>
            {{ t('disks.create') }}
          </button>
        </div>
      </form>
    </div>
    <button type="button" class="modal-backdrop" @click="open = false">{{ t('common.close') }}</button>
  </dialog>
</template>
