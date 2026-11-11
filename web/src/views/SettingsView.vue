<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { api, type Settings, type SettingsPatch } from '@/api'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useFleet } from '@/composables/useFleet'

type Tab = 'networking' | 'security' | 'guests'
type Pending = 'endpoint' | 'token' | 'regenerate'

const { t } = useI18n()
const { settings, refresh } = useFleet()

const tab = ref<Tab>('networking')
const endpoint = ref('')
const token = ref('')
const mtu = ref(1300)
const showToken = ref(false)
const pending = ref<Pending | null>(null)
const error = ref('')
const savedTab = ref<Tab | null>(null)

// Seed the form once; the 2s poll must not overwrite what the user is typing.
function seed(s: Settings) {
  endpoint.value = s.clients.endpoint
  token.value = s.token
  mtu.value = s.guestMTU
}
if (settings.value) seed(settings.value)
else {
  const stop = watch(settings, (s) => {
    if (!s) return
    seed(s)
    stop()
  })
}

const tabs: Tab[] = ['networking', 'security', 'guests']
const setupPending = computed(() => settings.value?.setupComplete === false)

async function apply(forTab: Tab, call: () => Promise<Settings>) {
  error.value = ''
  try {
    seed(await call())
    await refresh()
    savedTab.value = forTab
  } catch (e) {
    error.value = (e as Error).message
  }
}

const patch = (p: SettingsPatch) => () => api.updateSettings(p)

function saveEndpoint() {
  if ((settings.value?.devices ?? 0) > 0 && endpoint.value !== settings.value?.clients.endpoint) pending.value = 'endpoint'
  else void apply('networking', patch({ clients: { endpoint: endpoint.value } }))
}

function confirm() {
  const p = pending.value
  pending.value = null
  if (p === 'endpoint') void apply('networking', patch({ clients: { endpoint: endpoint.value } }))
  else if (p === 'token') void apply('security', patch({ token: token.value }))
  else if (p === 'regenerate') void apply('security', () => api.regenerateToken())
}

const copied = ref(false)
const tokenInput = ref<HTMLInputElement | null>(null)

async function copyToken() {
  try {
    await navigator.clipboard.writeText(token.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    showToken.value = true
    await nextTick()
    tokenInput.value?.select()
  }
}

const dialog = computed(() =>
  pending.value === 'endpoint'
    ? { title: t('settings.endpointChangeTitle'), body: t('settings.endpointChangeBody'), label: t('settings.confirm') }
    : { title: t('settings.tokenChangeTitle'), body: t('settings.tokenChangeBody'), label: t('settings.tokenChangeConfirm') },
)

watch(tab, () => {
  error.value = ''
  savedTab.value = null
})
</script>

<template>
  <div>
    <PageHeader section="settings" />

    <div v-if="setupPending" role="alert" class="alert alert-warning alert-soft mb-4 text-sm">
      <AppIcon name="warning" />
      {{ t('settings.setupBanner') }}
    </div>

    <DataState :loading="!settings" :empty="false" :empty-title="''">
      <div class="flex flex-col gap-6 md:flex-row">
        <ul class="menu menu-sm w-full gap-0.5 p-0 md:w-48 md:shrink-0" role="tablist">
          <li v-for="id in tabs" :key="id" role="presentation">
            <button
              :id="`tab-${id}`"
              type="button"
              role="tab"
              class="flex items-center gap-2"
              :class="tab === id ? 'menu-active' : ''"
              :aria-selected="tab === id"
              :aria-controls="`panel-${id}`"
              @click="tab = id"
            >
              <span class="grow text-left">{{ t(`settings.tabs.${id}`) }}</span>
              <AppIcon v-if="id === 'networking' && setupPending" name="warning" class="text-warning" />
            </button>
          </li>
        </ul>

        <div :id="`panel-${tab}`" role="tabpanel" :aria-labelledby="`tab-${tab}`" class="min-w-0 max-w-xl grow">
          <div v-if="error" role="alert" class="alert alert-error alert-soft mb-4 text-sm">{{ error }}</div>

          <form v-if="tab === 'networking'" class="flex flex-col gap-4" @submit.prevent="saveEndpoint">
            <label class="form-control">
              <span class="label-text mb-1 text-sm font-medium">{{ t('settings.endpoint') }}</span>
              <input v-model.trim="endpoint" type="text" class="input input-sm input-bordered w-full font-token" required />
              <span class="text-base-content/60 mt-1 text-xs">{{ t('settings.endpointHint') }}</span>
            </label>
            <div class="grid grid-cols-2 gap-3">
              <label v-for="[key, value] in [['listen', settings?.listen], ['hubPort', settings?.hub.port], ['underlayPort', settings?.underlay.port], ['clientsPort', settings?.clients.port]]" :key="key" class="form-control">
                <span class="label-text mb-1 text-sm font-medium">{{ t(`settings.${key}`) }}</span>
                <input :value="value" type="text" class="input input-sm input-bordered w-full font-token" readonly />
              </label>
            </div>
            <p class="text-base-content/60 text-xs">{{ t('settings.portsHint') }}</p>
            <div class="flex items-center gap-3">
              <button type="submit" class="btn btn-sm btn-primary">{{ t('settings.save') }}</button>
              <span v-if="savedTab === 'networking'" class="text-success text-sm">{{ t('settings.saved') }}</span>
            </div>
          </form>

          <form v-else-if="tab === 'security'" class="flex flex-col gap-4" @submit.prevent="pending = 'token'">
            <label class="form-control">
              <span class="label-text mb-1 text-sm font-medium">{{ t('settings.token') }}</span>
              <span class="flex gap-2">
                <input
                  ref="tokenInput"
                  v-model.trim="token"
                  :type="showToken ? 'text' : 'password'"
                  class="input input-sm input-bordered w-full font-token"
                  autocomplete="off"
                  required
                />
                <button type="button" class="btn btn-sm btn-ghost" @click="showToken = !showToken">
                  {{ showToken ? t('settings.hide') : t('settings.show') }}
                </button>
                <button type="button" class="btn btn-sm btn-ghost" :title="t('settings.copy')" @click="copyToken">
                  <AppIcon :name="copied ? 'check' : 'copy'" />
                </button>
              </span>
            </label>
            <div class="flex items-center gap-3">
              <button type="submit" class="btn btn-sm btn-primary" :disabled="token === settings?.token">
                {{ t('settings.save') }}
              </button>
              <button type="button" class="btn btn-sm btn-ghost" @click="pending = 'regenerate'">
                {{ t('settings.regenerate') }}
              </button>
              <span v-if="savedTab === 'security'" class="text-success text-sm">{{ t('settings.saved') }}</span>
            </div>
          </form>

          <form
            v-else
            class="flex flex-col gap-4"
            @submit.prevent="apply('guests', patch({ guestMTU: mtu }))"
          >
            <label class="form-control">
              <span class="label-text mb-1 text-sm font-medium">{{ t('settings.guestMTU') }}</span>
              <input v-model.number="mtu" type="number" min="576" max="9000" class="input input-sm input-bordered w-40" required />
              <span class="text-base-content/60 mt-1 text-xs">{{ t('settings.guestMTUHint') }}</span>
            </label>
            <div class="flex items-center gap-3">
              <button type="submit" class="btn btn-sm btn-primary">{{ t('settings.save') }}</button>
              <span v-if="savedTab === 'guests'" class="text-success text-sm">{{ t('settings.saved') }}</span>
            </div>
          </form>
        </div>
      </div>
    </DataState>

    <ConfirmDialog
      :open="pending !== null"
      :title="dialog.title"
      :body="dialog.body"
      :confirm-label="dialog.label"
      @confirm="confirm"
      @cancel="pending = null"
    />
  </div>
</template>
