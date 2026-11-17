<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { api, type Integration } from '@/api'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import IntegrationDialog from '@/components/IntegrationDialog.vue'
import PageHeader from '@/components/PageHeader.vue'

const { t, locale } = useI18n()

const items = ref<Integration[]>([])
const loaded = ref(false)
const error = ref('')
const editing = ref<Integration | null>(null)
const dialogOpen = ref(false)
const pendingDisconnect = ref<Integration | null>(null)

async function load() {
  try {
    items.value = await api.integrations()
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loaded.value = true
  }
}

function connect(i: Integration) {
  editing.value = i
  dialogOpen.value = true
}

async function disconnect() {
  const i = pendingDisconnect.value
  pendingDisconnect.value = null
  if (!i) return
  try {
    await api.deleteIntegration(i.name)
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader section="integrations" />

    <div v-if="error" role="alert" class="alert alert-error alert-soft mb-4 text-sm">{{ error }}</div>

    <DataState :loading="!loaded" :empty="false" :empty-title="''">
      <div class="grid gap-4 md:grid-cols-3">
        <section v-for="i in items" :key="i.name" class="card card-border bg-base-100">
          <div class="card-body gap-3 p-5">
            <div class="flex items-center justify-between gap-2">
              <h2 class="font-semibold">{{ t(`integrations.${i.name}.label`) }}</h2>
              <span class="badge badge-sm" :class="i.connected ? 'badge-success' : 'badge-ghost'">
                {{ t(i.connected ? 'integrations.connected' : 'integrations.notConnected') }}
              </span>
            </div>
            <p v-if="i.connected" class="text-sm">
              <span class="font-token">{{ i.account }}</span>
              <span class="text-base-content/60 block text-xs">{{ t('integrations.since', { time: new Date(i.connectedAt).toLocaleString(locale) }) }}</span>
            </p>
            <p class="text-base-content/65 text-xs leading-relaxed">{{ t(`integrations.${i.name}.howto`) }}</p>
            <div class="card-actions mt-auto justify-end">
              <button v-if="i.connected" type="button" class="btn btn-sm btn-ghost text-error" @click="pendingDisconnect = i">
                {{ t('integrations.disconnect') }}
              </button>
              <button type="button" class="btn btn-sm" :class="i.connected ? '' : 'btn-primary'" @click="connect(i)">
                {{ t(i.connected ? 'integrations.replace' : 'integrations.connect') }}
              </button>
            </div>
          </div>
        </section>
      </div>
    </DataState>

    <IntegrationDialog v-model:open="dialogOpen" :integration="editing" @saved="load" />
    <ConfirmDialog
      :open="pendingDisconnect !== null"
      :title="t('integrations.disconnectTitle', { name: pendingDisconnect ? t(`integrations.${pendingDisconnect.name}.label`) : '' })"
      :body="t('integrations.disconnectBody')"
      :confirm-label="t('integrations.disconnect')"
      @confirm="disconnect"
      @cancel="pendingDisconnect = null"
    />
  </div>
</template>
