<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import { THEME_PREFERENCES, type ThemePreference, useTheme } from '@/composables/useTheme'

import AppIcon from './AppIcon.vue'

const { t } = useI18n()
const { theme } = useTheme()

const ICONS: Record<ThemePreference, string> = { system: 'monitor', light: 'sun', dark: 'moon' }
</script>

<template>
  <div class="join" role="group" :aria-label="t('topbar.theme')">
    <button
      v-for="option in THEME_PREFERENCES"
      :key="option"
      type="button"
      class="btn join-item btn-xs"
      :class="theme === option ? 'btn-active' : 'btn-ghost'"
      :aria-pressed="theme === option"
      :title="option"
      @click="theme = option"
    >
      <AppIcon :name="ICONS[option]" />
      <span class="sr-only">{{ option }}</span>
    </button>
  </div>
</template>
