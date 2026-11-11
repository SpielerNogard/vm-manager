<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute } from 'vue-router'

import ChassisBadge from '@/components/ChassisBadge.vue'
import DataState from '@/components/DataState.vue'
import MeterBar from '@/components/MeterBar.vue'
import StateBadge from '@/components/StateBadge.vue'
import VMTable from '@/components/VMTable.vue'
import { useFleet } from '@/composables/useFleet'
import { flatten, formatMB } from '@/lib/format'

const { t, locale } = useI18n()
const route = useRoute()
const { nodes, vms, loaded } = useFleet()

const node = computed(() => nodes.value.find((n) => n.id === route.params.id))
const nodeVMs = computed(() => vms.value.filter((vm) => vm.nodeId === route.params.id))
const config = computed(() => flatten(node.value?.config ?? {}))
const connectedAt = computed(() => node.value && new Date(node.value.connectedAt).toLocaleString(locale.value))
const formatCPUs = (n: number) => `${n} vCPU`
</script>

<template>
  <div>
    <nav class="text-base-content/65 mb-2 text-sm">
      <RouterLink :to="{ name: 'nodes' }" class="hover:underline">{{ t('nav.nodes.label') }}</RouterLink>
      <span aria-hidden="true"> / </span>
    </nav>

    <div v-if="!loaded" class="flex justify-center py-16"><span class="loading loading-spinner"></span></div>

    <div v-else-if="!node" class="border-base-300 rounded-box border border-dashed px-6 py-12 text-center">
      <p class="font-medium">{{ t('node.notFound', { id: route.params.id }) }}</p>
    </div>

    <template v-else>
      <header class="mb-6">
        <div class="flex flex-wrap items-center gap-3">
          <h1 class="text-2xl font-semibold tracking-tight">{{ node.id }}</h1>
          <StateBadge :label="t(node.online ? 'state.online' : 'state.offline')" :tone="node.online ? 'good' : 'quiet'" />
          <span class="badge badge-sm badge-outline font-token">{{ node.provider }}</span>
        </div>
        <p class="text-base-content/65 mt-1 text-sm">
          <span class="font-token">{{ node.hostname }}</span>
          <template v-if="connectedAt"> · {{ t('node.connectedSince', { time: connectedAt }) }}</template>
        </p>
      </header>

      <div class="mb-6 grid gap-4 lg:grid-cols-2">
        <section class="border-base-300 rounded-box space-y-4 border p-4">
          <h2 class="font-semibold">{{ t('node.capacity') }}</h2>
          <MeterBar :label="t('node.cpu')" :used="node.cpus - node.freeCpus" :total="node.cpus" :format="formatCPUs" />
          <MeterBar :label="t('node.memory')" :used="node.memoryMB - node.freeMemoryMB" :total="node.memoryMB" :format="formatMB" />
          <p class="text-base-content/60 text-xs">{{ t('node.capacityHint') }}</p>
        </section>

        <section class="border-base-300 rounded-box border p-4">
          <h2 class="mb-3 font-semibold">{{ t('node.machine') }}</h2>
          <dl v-if="node.machine" class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-sm">
            <dt class="text-base-content/60">{{ t('node.os') }}</dt>
            <dd>{{ node.machine.os === 'darwin' ? 'macOS' : node.machine.os }} {{ node.machine.osVersion }}</dd>
            <dt class="text-base-content/60">{{ t('node.arch') }}</dt>
            <dd class="font-token">{{ node.machine.arch }}</dd>
            <template v-if="node.machine.cpuModel">
              <dt class="text-base-content/60">{{ t('node.cpuModel') }}</dt>
              <dd>{{ node.machine.cpuModel }}</dd>
            </template>
            <template v-if="node.chassis">
              <dt class="text-base-content/60">{{ t('node.chassis') }}</dt>
              <dd class="flex flex-wrap items-center gap-2">
                <span class="font-token">{{ node.chassis.ip }}</span>
                <ChassisBadge :chassis="node.chassis" />
              </dd>
            </template>
            <dt class="text-base-content/60">{{ t('node.hardware') }}</dt>
            <dd class="tabular-nums">
              {{ node.machine.cpus }} vCPU<template v-if="node.machine.memoryMB"> · {{ formatMB(node.machine.memoryMB) }}</template>
            </dd>
          </dl>
          <p v-else class="text-base-content/60 text-sm">{{ t('node.notReported') }}</p>
        </section>
      </div>

      <section class="border-base-300 rounded-box mb-6 border p-4">
        <h2 class="font-semibold">{{ t('node.config') }}</h2>
        <p class="text-base-content/60 mb-3 text-xs">{{ t('node.configHint') }}</p>
        <table v-if="config.length" class="table-sm table">
          <tbody>
            <tr v-for="[key, value] in config" :key="key">
              <td class="font-token text-base-content/65 w-0 whitespace-nowrap">{{ key }}</td>
              <td class="font-token break-all">{{ value }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="text-base-content/60 text-sm">{{ t('node.notReported') }}</p>
      </section>

      <h2 class="mb-3 font-semibold">{{ t('node.vms') }}</h2>
      <DataState :loading="false" :empty="!nodeVMs.length" :empty-title="t('node.noVMs')">
        <VMTable :vms="nodeVMs" />
      </DataState>
    </template>
  </div>
</template>
