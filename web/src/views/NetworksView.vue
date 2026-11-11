<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { api, type Network } from '@/api'
import AddNetworkDialog from '@/components/AddNetworkDialog.vue'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useFleet } from '@/composables/useFleet'

const { t } = useI18n()
const { networks, loaded, refresh } = useFleet()

const addOpen = ref(false)
const pendingDelete = ref<Network | null>(null)
const error = ref('')

const deleteBlocked = (n: Network) => n.builtin || n.members.length > 0

async function deleteNetwork() {
  const n = pendingDelete.value
  pendingDelete.value = null
  if (!n) return
  error.value = ''
  try {
    await api.deleteNetwork(n.id)
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  }
}
</script>

<template>
  <div>
    <PageHeader section="networks">
      <template #actions>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('networks.add') }}
        </button>
      </template>
    </PageHeader>

    <div v-if="error" role="alert" class="alert alert-error alert-soft mb-4 text-sm">{{ error }}</div>

    <DataState :loading="!loaded" :empty="!networks.length" :empty-title="t('networks.emptyTitle')">
      <template #empty-body>{{ t('networks.emptyBody') }}</template>
      <template #empty-action>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('networks.add') }}
        </button>
      </template>

      <table class="table-sm table">
        <thead>
          <tr>
            <th>{{ t('common.name') }}</th>
            <th>{{ t('networks.subnet') }}</th>
            <th>{{ t('networks.members') }}</th>
            <th class="w-0"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="n in networks" :key="n.id">
            <td class="font-medium">
              {{ n.name }}<span v-if="n.builtin" class="badge badge-xs badge-accent ml-2">{{ t('networks.builtin') }}</span>
            </td>
            <td class="font-token text-sm">{{ n.subnet }}</td>
            <td class="tabular-nums">{{ n.members.length }}</td>
            <td>
              <span :title="n.members.length ? t('networks.inUse') : n.builtin ? t('networks.builtin') : undefined">
                <button
                  type="button"
                  class="btn btn-ghost btn-xs text-error"
                  :disabled="deleteBlocked(n)"
                  @click="pendingDelete = n"
                >
                  {{ t('common.delete') }}
                </button>
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </DataState>

    <AddNetworkDialog v-model:open="addOpen" />
    <ConfirmDialog
      :open="pendingDelete !== null"
      :title="t('networks.deleteTitle', { name: pendingDelete?.name })"
      :body="t('networks.deleteBody')"
      :confirm-label="t('networks.deleteConfirm')"
      @confirm="deleteNetwork"
      @cancel="pendingDelete = null"
    />
  </div>
</template>
