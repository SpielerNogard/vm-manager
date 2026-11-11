<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute } from 'vue-router'

import { useFleet } from '@/composables/useFleet'
import { NAV, type Section } from '@/nav'

import AppIcon from './AppIcon.vue'

const { t } = useI18n()
const { nodes, vms, keys, devices, networks, settings } = useFleet()

const locked = (section: Section) => section !== 'settings' && settings.value?.setupComplete === false

const route = useRoute()

const count = (section: Section) => ({ nodes, vms, keys, devices, networks, settings: ref([]) })[section].value.length
const isActive = (section: Section) => route.name === section || route.meta.section === section
</script>

<template>
  <nav class="flex flex-col gap-5 p-3">
    <div v-for="group in NAV" :key="group.group">
      <p class="text-base-content/50 px-3 pb-1 text-xs font-semibold tracking-widest uppercase">
        {{ t(`nav.${group.group}`) }}
      </p>
      <ul class="menu menu-sm w-full gap-0.5 p-0">
        <li v-for="item in group.items" :key="item.name">
          <span v-if="locked(item.name)" class="flex items-center gap-2.5 opacity-40" aria-disabled="true">
            <AppIcon :name="item.icon" />
            <span class="grow">{{ t(`nav.${item.name}.label`) }}</span>
            <AppIcon name="lock" />
          </span>
          <RouterLink
            v-else
            :to="item.path"
            class="flex items-center gap-2.5"
            :class="isActive(item.name) ? 'menu-active' : ''"
            :aria-current="isActive(item.name) ? 'page' : undefined"
          >
            <AppIcon :name="item.icon" class="opacity-70" />
            <span class="grow">{{ t(`nav.${item.name}.label`) }}</span>
            <span v-if="item.name !== 'settings'" class="text-xs tabular-nums opacity-60">{{ count(item.name) }}</span>
          </RouterLink>
        </li>
      </ul>
    </div>
  </nav>
</template>
