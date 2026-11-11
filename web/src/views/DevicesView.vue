<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { api, type Device } from '@/api'
import ActionsMenu from '@/components/ActionsMenu.vue'
import AddDeviceDialog from '@/components/AddDeviceDialog.vue'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useFleet } from '@/composables/useFleet'
import { downloadText } from '@/lib/format'

const { t, locale } = useI18n()
const { devices, loaded, refresh } = useFleet()

const addOpen = ref(false)
const pendingDelete = ref<Device | null>(null)
const error = ref('')

async function download(d: Device) {
  error.value = ''
  try {
    downloadText(`${d.name}.conf`, await api.deviceConfig(d.id))
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function deleteDevice() {
  const d = pendingDelete.value
  pendingDelete.value = null
  if (!d) return
  error.value = ''
  try {
    await api.deleteDevice(d.id)
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  }
}
</script>

<template>
  <div>
    <PageHeader section="devices">
      <template #actions>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('devices.add') }}
        </button>
      </template>
    </PageHeader>

    <div v-if="error" role="alert" class="alert alert-error alert-soft mb-4 text-sm">{{ error }}</div>

    <DataState :loading="!loaded" :empty="!devices.length" :empty-title="t('devices.emptyTitle')">
      <template #empty-body>{{ t('devices.emptyBody') }}</template>
      <template #empty-action>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('devices.add') }}
        </button>
      </template>

      <table class="table-sm table">
        <thead>
          <tr>
            <th>{{ t('common.name') }}</th>
            <th>{{ t('devices.ip') }}</th>
            <th>{{ t('devices.created') }}</th>
            <th class="w-0"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in devices" :key="d.id">
            <td class="font-medium">{{ d.name }}</td>
            <td class="font-token text-sm">{{ d.ip }}</td>
            <td class="text-sm">{{ new Date(d.createdAt).toLocaleString(locale) }}</td>
            <td>
              <ActionsMenu :name="d.name">
                <li><button type="button" @click="download(d)">{{ t('devices.download') }}</button></li>
                <li><button type="button" class="text-error" @click="pendingDelete = d">{{ t('common.delete') }}</button></li>
              </ActionsMenu>
            </td>
          </tr>
        </tbody>
      </table>
    </DataState>

    <AddDeviceDialog v-model:open="addOpen" />
    <ConfirmDialog
      :open="pendingDelete !== null"
      :title="t('devices.deleteTitle', { name: pendingDelete?.name })"
      :body="t('devices.deleteBody')"
      :confirm-label="t('devices.deleteConfirm')"
      @confirm="deleteDevice"
      @cancel="pendingDelete = null"
    />
  </div>
</template>
