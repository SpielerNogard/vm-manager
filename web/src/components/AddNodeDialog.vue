<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import { api } from '@/api'
import { useFleet } from '@/composables/useFleet'
import { NAME_PATTERN } from '@/lib/names'

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const router = useRouter()
const { refresh } = useFleet()

const name = ref('')
const provider = ref('')
const error = ref('')
const busy = ref(false)

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  provider.value = ''
  error.value = ''
})

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const node = await api.createNode(name.value, provider.value)
    open.value = false
    await refresh()
    await router.push({ name: 'node', params: { id: node.id } })
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
      <h3 class="text-lg font-semibold tracking-tight">{{ t('nodes.dialogTitle') }}</h3>
      <form class="mt-4 space-y-4" @submit.prevent="submit">
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('common.name') }}</span>
          <input
            v-model="name"
            class="input w-full"
            required
            maxlength="63"
            :pattern="NAME_PATTERN"
            placeholder="mac-mini"
          />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('nodes.nameHint') }}</span>
        </label>
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('nodes.provider') }}</span>
          <select v-model="provider" class="select w-full" required>
            <option value="" disabled>{{ t('nodes.providerPlaceholder') }}</option>
            <option value="vz">vz</option>
            <option value="firecracker">firecracker</option>
            <option value="cloud-hypervisor">cloud-hypervisor</option>
          </select>
          <span class="text-base-content/60 mt-1 block text-xs">{{ t('nodes.providerHint') }}</span>
        </label>
        <div v-if="error" role="alert" class="alert alert-error alert-soft text-sm">{{ error }}</div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost" @click="open = false">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
            <span v-if="busy" class="loading loading-spinner loading-xs"></span>
            {{ t('nodes.create') }}
          </button>
        </div>
      </form>
    </div>
    <button type="button" class="modal-backdrop" @click="open = false">{{ t('common.close') }}</button>
  </dialog>
</template>
