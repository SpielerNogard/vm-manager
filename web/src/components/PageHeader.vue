<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import type { Section } from '@/nav'

defineProps<{ section: Section }>()

const { t } = useI18n()
</script>

<template>
  <header class="mb-6 flex flex-wrap items-start justify-between gap-4">
    <div class="min-w-0">
      <h1 class="text-2xl font-semibold tracking-tight">{{ t(`nav.${section}.label`) }}</h1>
      <p class="text-base-content/65 mt-1 max-w-2xl text-sm leading-relaxed">{{ t(`nav.${section}.blurb`) }}</p>
    </div>
    <div class="flex shrink-0 items-center gap-2"><slot name="actions" /></div>
  </header>
</template>
