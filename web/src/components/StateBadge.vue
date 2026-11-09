<script setup lang="ts">
import { TONE_DOT, type Tone } from '@/lib/format'

defineProps<{ label: string; tone: Tone; reason?: string }>()
</script>

<template>
  <span class="inline-flex items-center gap-1.5" :title="reason || undefined">
    <span class="size-1.5 shrink-0 rounded-full" :class="TONE_DOT[tone]" aria-hidden="true"></span>
    <span>{{ label }}</span>
  </span>
</template>
