<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { api } from '@/api'
import { useFleet } from '@/composables/useFleet'

import AppIcon from './AppIcon.vue'

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const { refresh } = useFleet()

const name = ref('')
const publicKey = ref('')
const error = ref('')
const busy = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

watch(open, (isOpen) => {
  if (!isOpen) return
  name.value = ''
  publicKey.value = ''
  error.value = ''
})

async function load(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  publicKey.value = (await file.text()).trim()
  // Default the name to the key's comment, else the file name.
  name.value ||= publicKey.value.split(/\s+/)[2] ?? file.name.replace(/\.pub$/, '')
}

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.addKey(name.value, publicKey.value)
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
      <h3 class="text-lg font-semibold tracking-tight">{{ t('keys.dialogTitle') }}</h3>
      <form class="mt-4 space-y-4" @submit.prevent="submit">
        <label class="block">
          <span class="mb-1 block text-sm font-medium">{{ t('common.name') }}</span>
          <input v-model="name" class="input w-full" required placeholder="MacBook" />
        </label>
        <div>
          <div class="mb-1 flex items-center justify-between gap-2">
            <label for="public-key" class="text-sm font-medium">{{ t('keys.publicKey') }}</label>
            <button type="button" class="btn btn-ghost btn-xs" @click="fileInput?.click()">
              <AppIcon name="upload" />
              {{ t('keys.upload') }}
            </button>
            <input ref="fileInput" type="file" accept=".pub" class="hidden" @change="load" />
          </div>
          <textarea
            id="public-key"
            v-model="publicKey"
            class="textarea font-token w-full text-xs"
            rows="4"
            required
            placeholder="ssh-ed25519 AAAA… user@host"
          ></textarea>
          <i18n-t keypath="keys.publicKeyHint" tag="span" class="text-base-content/60 mt-1 block text-xs">
            <template #ext><code class="font-token">.pub</code></template>
            <template #cmd><code class="font-token">pbcopy &lt; ~/.ssh/id_ed25519.pub</code></template>
          </i18n-t>
        </div>
        <div v-if="error" role="alert" class="alert alert-error alert-soft text-sm">{{ error }}</div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost" @click="open = false">{{ t('common.cancel') }}</button>
          <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
            <span v-if="busy" class="loading loading-spinner loading-xs"></span>
            {{ t('keys.save') }}
          </button>
        </div>
      </form>
    </div>
    <button type="button" class="modal-backdrop" @click="open = false">{{ t('common.close') }}</button>
  </dialog>
</template>
