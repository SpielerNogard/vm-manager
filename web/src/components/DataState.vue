<script setup lang="ts">
// Every list goes through here, so loading and emptiness look the same on every page.
defineProps<{ loading: boolean; empty?: boolean; emptyTitle?: string }>()
</script>

<template>
  <div v-if="loading" class="flex justify-center py-16" aria-busy="true">
    <span class="loading loading-spinner"></span>
  </div>

  <div
    v-else-if="empty"
    class="border-base-300 rounded-box flex flex-col items-center gap-1 border border-dashed px-6 py-12 text-center"
  >
    <p class="font-medium">{{ emptyTitle }}</p>
    <div class="text-base-content/65 max-w-md text-sm leading-relaxed"><slot name="empty-body" /></div>
    <div class="mt-3 empty:hidden"><slot name="empty-action" /></div>
  </div>

  <div v-else class="border-base-300 rounded-box overflow-x-auto border">
    <slot />
  </div>
</template>
