<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{ open: boolean; title: string; body: string; confirmLabel: string }>()
const emit = defineEmits<{ confirm: []; cancel: [] }>()

const { t } = useI18n()
</script>

<template>
  <dialog class="modal" :open="open">
    <div class="modal-box">
      <h3 class="text-lg font-semibold tracking-tight">{{ title }}</h3>
      <p class="mt-2 text-sm leading-relaxed">{{ body }}</p>
      <div class="modal-action">
        <button type="button" class="btn btn-sm btn-ghost" @click="emit('cancel')">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-sm btn-error" @click="emit('confirm')">{{ confirmLabel }}</button>
      </div>
    </div>
  </dialog>
</template>
