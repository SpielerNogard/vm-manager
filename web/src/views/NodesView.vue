<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { RouterLink, useRouter } from 'vue-router'

import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StateBadge from '@/components/StateBadge.vue'
import { useFleet } from '@/composables/useFleet'
import { formatMB } from '@/lib/format'

const { t } = useI18n()
const router = useRouter()
const { nodes, loaded } = useFleet()
</script>

<template>
  <div>
    <PageHeader section="nodes" />

    <DataState :loading="!loaded" :empty="!nodes.length" :empty-title="t('nodes.emptyTitle')">
      <template #empty-body>{{ t('nodes.emptyBody') }}</template>

      <table class="table-sm table">
        <thead>
          <tr>
            <th>{{ t('nodes.node') }}</th>
            <th>{{ t('common.status') }}</th>
            <th>{{ t('nodes.provider') }}</th>
            <th class="text-right">{{ t('nodes.capacity') }}</th>
            <th class="text-right">{{ t('nodes.free') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="n in nodes"
            :key="n.id"
            class="hover:bg-base-200/50 cursor-pointer"
            @click="router.push({ name: 'node', params: { id: n.id } })"
          >
            <td>
              <!-- A real link as well as the row handler, for middle-click and screen readers. -->
              <RouterLink
                :to="{ name: 'node', params: { id: n.id } }"
                class="font-medium hover:underline"
                :aria-label="t('node.open', { name: n.id })"
                @click.stop
              >
                {{ n.id }}
              </RouterLink>
              <div class="font-token text-base-content/55 text-[0.7rem]">{{ n.hostname }}</div>
            </td>
            <td>
              <StateBadge :label="t(n.online ? 'state.online' : 'state.offline')" :tone="n.online ? 'good' : 'quiet'" />
            </td>
            <td><span class="badge badge-sm badge-outline font-token">{{ n.provider }}</span></td>
            <td class="text-right text-sm tabular-nums">
              {{ n.cpus }} vCPU<br /><span class="text-base-content/55 text-xs">{{ formatMB(n.memoryMB) }}</span>
            </td>
            <td class="text-right text-sm tabular-nums">
              {{ n.freeCpus }} vCPU<br /><span class="text-base-content/55 text-xs">{{ formatMB(n.freeMemoryMB) }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </DataState>
  </div>
</template>
