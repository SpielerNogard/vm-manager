<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { LOCALES, type Locale } from '@/i18n'

const { t, locale } = useI18n()
const current = computed(() => LOCALES.find((l) => l.code === locale.value) ?? LOCALES[0])

function select(code: Locale) {
  locale.value = code
  // daisyUI dropdowns stay open while they hold focus.
  ;(document.activeElement as HTMLElement | null)?.blur()
}
</script>

<template>
  <div class="dropdown dropdown-end">
    <div tabindex="0" role="button" class="btn btn-ghost btn-sm gap-2" :aria-label="t('topbar.language')">
      <span aria-hidden="true">{{ current.flag }}</span>
      <span class="hidden sm:inline">{{ current.name }}</span>
    </div>
    <ul
      tabindex="0"
      class="dropdown-content menu menu-sm bg-base-100 rounded-box border-base-300 z-30 mt-2 w-44 border p-2 shadow-lg"
    >
      <li v-for="l in LOCALES" :key="l.code">
        <button
          type="button"
          :class="locale === l.code ? 'menu-active' : ''"
          :aria-current="locale === l.code || undefined"
          @click="select(l.code)"
        >
          <span aria-hidden="true">{{ l.flag }}</span>
          {{ l.name }}
        </button>
      </li>
    </ul>
  </div>
</template>
