<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{ name: string }>()

const { t } = useI18n()
</script>

<template>
  <div class="dropdown dropdown-end">
    <div tabindex="0" role="button" class="btn btn-ghost btn-xs">
      ⋯<span class="sr-only">{{ t('common.actionsFor', { name }) }}</span>
    </div>
    <ul
      tabindex="0"
      class="dropdown-content menu menu-sm bg-base-100 rounded-box border-base-300 z-30 w-44 border p-2 shadow-lg"
    >
      <slot />
    </ul>
  </div>
</template>
