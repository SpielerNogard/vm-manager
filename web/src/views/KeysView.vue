<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { api, type SSHKey } from '@/api'
import ActionsMenu from '@/components/ActionsMenu.vue'
import AddKeyDialog from '@/components/AddKeyDialog.vue'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useFleet } from '@/composables/useFleet'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { keys, loaded, refresh } = useFleet()

const addOpen = ref(false)
const pendingDelete = ref<SSHKey | null>(null)
const error = ref('')

// The create-VM dialog links here with ?add=1 to open the add dialog straight away.
watch(
  () => route.query.add,
  (add) => {
    if (!add) return
    addOpen.value = true
    void router.replace({ query: {} })
  },
  { immediate: true },
)

async function deleteKey() {
  const k = pendingDelete.value
  pendingDelete.value = null
  if (!k) return
  error.value = ''
  try {
    await api.deleteKey(k.id)
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  }
}
</script>

<template>
  <div>
    <PageHeader section="keys">
      <template #actions>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('keys.add') }}
        </button>
      </template>
    </PageHeader>

    <div v-if="error" role="alert" class="alert alert-error alert-soft mb-4 text-sm">{{ error }}</div>

    <DataState :loading="!loaded" :empty="!keys.length" :empty-title="t('keys.emptyTitle')">
      <template #empty-body>
        <i18n-t keypath="keys.emptyBody" tag="span">
          <template #file><code class="font-token">~/.ssh/id_ed25519.pub</code></template>
        </i18n-t>
      </template>
      <template #empty-action>
        <button type="button" class="btn btn-sm btn-primary" @click="addOpen = true">
          <AppIcon name="plus" />
          {{ t('keys.add') }}
        </button>
      </template>

      <table class="table-sm table">
        <thead>
          <tr>
            <th>{{ t('common.name') }}</th>
            <th>{{ t('keys.type') }}</th>
            <th>{{ t('keys.fingerprint') }}</th>
            <th class="w-0"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="k in keys" :key="k.id">
            <td>
              <span class="font-medium">{{ k.name }}</span>
              <span v-if="k.hub" class="badge badge-sm badge-accent ml-2" :title="t('keys.hubHint')">{{ t('keys.hub') }}</span>
            </td>
            <td><span class="badge badge-sm badge-outline font-token">{{ k.type }}</span></td>
            <td class="font-token text-xs">{{ k.fingerprint }}</td>
            <td>
              <ActionsMenu v-if="!k.hub" :name="k.name">
                <li>
                  <button type="button" class="text-error" @click="pendingDelete = k">{{ t('common.delete') }}</button>
                </li>
              </ActionsMenu>
            </td>
          </tr>
        </tbody>
      </table>
    </DataState>

    <AddKeyDialog v-model:open="addOpen" />
    <ConfirmDialog
      :open="pendingDelete !== null"
      :title="t('keys.deleteTitle', { name: pendingDelete?.name })"
      :body="t('keys.deleteBody')"
      :confirm-label="t('keys.deleteConfirm')"
      @confirm="deleteKey"
      @cancel="pendingDelete = null"
    />
  </div>
</template>
