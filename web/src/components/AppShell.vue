<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { RouterView } from 'vue-router'

import { useFleet } from '@/composables/useFleet'

import AppIcon from './AppIcon.vue'
import LocaleSelect from './LocaleSelect.vue'
import SideNav from './SideNav.vue'
import ThemeToggle from './ThemeToggle.vue'

const { t } = useI18n()
const { loadError } = useFleet()
</script>

<template>
  <div class="drawer lg:drawer-open">
    <input id="nav-drawer" type="checkbox" class="drawer-toggle" />

    <div class="drawer-content flex min-h-dvh flex-col">
      <header
        class="border-base-300 bg-base-100/85 sticky top-0 z-20 flex h-14 items-center gap-3 border-b px-4 backdrop-blur"
      >
        <label for="nav-drawer" class="btn btn-ghost btn-sm lg:hidden">
          <span class="sr-only">{{ t('topbar.menu') }}</span>
          <AppIcon name="menu" />
        </label>
        <span class="text-base font-semibold tracking-tight lg:hidden">vm-manager</span>
        <div class="grow"></div>
        <LocaleSelect />
        <ThemeToggle />
      </header>

      <main class="grow px-4 py-6 sm:px-6 lg:px-8">
        <div v-if="loadError" role="alert" class="alert alert-error alert-soft mb-4 text-sm">
          {{ t('common.hubUnreachable', { error: loadError.message }) }}
        </div>
        <RouterView />
      </main>
    </div>

    <div class="drawer-side z-30">
      <label for="nav-drawer" :aria-label="t('topbar.closeMenu')" class="drawer-overlay"></label>
      <aside class="bg-base-200 border-base-300 flex min-h-dvh w-64 flex-col border-r">
        <div class="border-base-300 flex h-14 shrink-0 items-center gap-2 border-b px-4">
          <span class="bg-accent size-2.5 shrink-0 rounded-sm"></span>
          <span class="text-base font-semibold tracking-tight">vm-manager</span>
        </div>
        <SideNav />
      </aside>
    </div>
  </div>
</template>
