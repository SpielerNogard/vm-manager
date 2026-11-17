<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { api, type Integration } from '@/api'

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ integration: Integration | null }>()
const emit = defineEmits<{ saved: [] }>()

const { t } = useI18n()

const secret = ref('')
const error = ref('')
const busy = ref(false)

// Cleared on close too, so a cancelled or failed secret does not stay in memory.
watch(open, () => {
  secret.value = ''
  error.value = ''
})

async function submit() {
  const i = props.integration
  if (!i) return
  error.value = ''
  busy.value = true
  try {
    await api.setIntegration(i.name, i.source, secret.value.trim())
    open.value = false
    emit('saved')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <dialog class="modal" :open="open" @close="open = false">
    <div v-if="integration" class="modal-box max-w-xl">
      <h3 class="text-lg font-semibold tracking-tight">
        {{ t(integration.connected ? 'integrations.replaceTitle' : 'integrations.connectTitle', { name: t(`integrations.${integration.name}.label`) }) }}
      </h3>
      <form class="mt-4 space-y-4" @submit.prevent="submit">
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t(`integrations.${integration.name}.secret`) }}</span>
          <input
            v-model="secret"
            type="password"
            class="input font-token w-full"
            required
            autocomplete="off"
            spellcheck="false"
          />
          <span class="text-base-content/60 mt-1 block text-xs">{{ t(`integrations.${integration.name}.howto`) }}</span>
        </label>
        <div v-if="error" role="alert" class="alert alert-error alert-soft text-sm">{{ error }}</div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost" @click="open = false">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
            <span v-if="busy" class="loading loading-spinner loading-xs"></span>
            {{ t('integrations.save') }}
          </button>
        </div>
      </form>
    </div>
    <button type="button" class="modal-backdrop" @click="open = false">{{ t('common.close') }}</button>
  </dialog>
</template>
