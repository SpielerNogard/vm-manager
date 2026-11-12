<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { api } from '@/api'
import { useFleet } from '@/composables/useFleet'
import { NAME_PATTERN } from '@/lib/names'

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const { refresh } = useFleet()

const name = ref('')
const subnet = ref('')
const error = ref('')
const busy = ref(false)

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  subnet.value = ''
  error.value = ''
})

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.createNetwork({ name: name.value, subnet: subnet.value.trim() || undefined })
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
      <h3 class="text-lg font-semibold tracking-tight">{{ t('networks.dialogTitle') }}</h3>
      <form class="mt-4 space-y-4" @submit.prevent="submit">
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('common.name') }}</span>
          <input
            v-model="name"
            class="input w-full"
            required
            maxlength="63"
            :pattern="NAME_PATTERN"
            placeholder="backend"
          />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('networks.nameHint') }}</span>
        </label>
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('networks.subnet') }}</span>
          <input v-model="subnet" class="input font-token w-full" :placeholder="t('networks.automatic')" />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('networks.subnetHint') }}</span>
        </label>
        <div v-if="error" role="alert" class="alert alert-error alert-soft text-sm">{{ error }}</div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost" @click="open = false">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
            <span v-if="busy" class="loading loading-spinner loading-xs"></span>
            {{ t('networks.create') }}
          </button>
        </div>
      </form>
    </div>
    <button type="button" class="modal-backdrop" @click="open = false">{{ t('common.close') }}</button>
  </dialog>
</template>
