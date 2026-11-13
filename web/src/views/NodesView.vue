<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRouter } from 'vue-router'

import AddNodeDialog from '@/components/AddNodeDialog.vue'
import AppIcon from '@/components/AppIcon.vue'
import ChassisBadge from '@/components/ChassisBadge.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StateBadge from '@/components/StateBadge.vue'
import { useFleet } from '@/composables/useFleet'
import { formatMB } from '@/lib/format'

const { t } = useI18n()
const router = useRouter()
const { nodes, loaded } = useFleet()

const addOpen = ref(false)
</script>

<template>
  <div>
    <PageHeader section="nodes">
      <template #actions>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('nodes.add') }}
        </button>
      </template>
    </PageHeader>

    <DataState :loading="!loaded" :empty="!nodes.length" :empty-title="t('nodes.emptyTitle')">
      <template #empty-body>{{ t('nodes.emptyBody') }}</template>

      <table class="table-sm table">
        <thead>
          <tr>
            <th>{{ t('nodes.node') }}</th>
            <th>{{ t('common.status') }}</th>
            <th>{{ t('nodes.chassis') }}</th>
            <th>{{ t('nodes.provider') }}</th>
            <th>{{ t('nodes.version') }}</th>
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
              <div v-if="n.hostname" class="font-token text-base-content/55 text-[0.7rem]">{{ n.hostname }}</div>
            </td>
            <td>
              <StateBadge
                :label="n.state === 'pending' ? t('nodes.pendingLabel') : t(`state.${n.state}`)"
                :tone="n.state === 'online' ? 'good' : 'quiet'"
              />
            </td>
            <td>
              <template v-if="n.chassis">
                <ChassisBadge :chassis="n.chassis" />
                <div class="font-token text-base-content/55 text-[0.7rem]">{{ n.chassis.ip }}</div>
              </template>
              <span v-else class="text-base-content/50 text-xs">—</span>
            </td>
            <td>
              <span v-if="n.provider" class="badge badge-sm badge-outline font-token">{{ n.provider }}</span>
              <span v-else class="text-base-content/50 text-xs">—</span>
            </td>
            <td class="text-sm">
              <span v-if="n.state === 'pending'" class="text-base-content/50 text-xs">—</span>
              <template v-else>
                <span v-if="n.version" class="font-token">{{ n.version }}</span>
                <span v-else class="text-base-content/60">{{ t('nodes.versionUnknown') }}</span>
                <div v-if="n.updateAvailable" class="text-xs"><StateBadge :label="t('nodes.updateAvailable')" tone="warning" /></div>
              </template>
            </td>
            <td class="text-right text-sm tabular-nums">
              <span v-if="n.state === 'pending'" class="text-base-content/50">—</span>
              <template v-else>
                {{ n.cpus }} vCPU<br /><span class="text-base-content/55 text-xs">{{ formatMB(n.memoryMB) }}</span>
              </template>
            </td>
            <td class="text-right text-sm tabular-nums">
              <span v-if="n.state === 'pending'" class="text-base-content/50">—</span>
              <template v-else>
                {{ n.freeCpus }} vCPU<br /><span class="text-base-content/55 text-xs">{{ formatMB(n.freeMemoryMB) }}</span>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
    </DataState>

    <AddNodeDialog v-model:open="addOpen" />
  </div>
</template>
