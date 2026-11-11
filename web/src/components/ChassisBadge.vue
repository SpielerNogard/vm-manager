<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { Chassis } from '@/api'

import StateBadge from './StateBadge.vue'

const props = defineProps<{ chassis: Chassis }>()
const { t } = useI18n()

// WireGuard re-handshakes about every 2 minutes while traffic flows.
const FRESH_SECONDS = 180

const badge = computed(() => {
  const c = props.chassis
  if (!c.registered) return { label: t('chassis.notRegistered'), tone: 'warning' as const }
  if (!c.lastHandshake) return { label: t('chassis.noHandshake'), tone: 'quiet' as const }
  const age = Math.max(0, Math.round((Date.now() - new Date(c.lastHandshake).getTime()) / 1000))
  if (age <= FRESH_SECONDS) return { label: t('chassis.connected'), tone: 'good' as const }
  return { label: t('chassis.handshakeAgo', { n: age }), tone: 'warning' as const }
})
</script>

<template>
  <StateBadge :label="badge.label" :tone="badge.tone" />
</template>
