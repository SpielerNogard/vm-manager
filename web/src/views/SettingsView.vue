<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { api, type Settings, type SettingsPatch } from '@/api'
import AppIcon from '@/components/AppIcon.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { useFleet } from '@/composables/useFleet'

type Tab = 'networking' | 'security' | 'guests' | 'updates'
type Pending = 'endpoint' | 'token' | 'regenerate'

const { t, locale } = useI18n()
const { settings, updates, loaded, refresh } = useFleet()

const tab = ref<Tab>('networking')
const endpoint = ref('')
const storageEndpoint = ref('')
const token = ref('')
const mtu = ref(1300)
const showToken = ref(false)
const pending = ref<Pending | null>(null)
const error = ref('')
const savedTab = ref<Tab | null>(null)

// Seed the form once; the 2s poll must not overwrite what the user is typing.
function seed(s: Settings) {
  endpoint.value = s.clients.endpoint
  storageEndpoint.value = s.storage.endpoint ?? ''
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

const tabs: Tab[] = ['networking', 'security', 'guests', 'updates']
const setupPending = computed(() => settings.value?.setupComplete === false)
const ports = computed(() => [
  { key: 'listen', value: settings.value?.listen },
  { key: 'hubPort', value: settings.value?.hub.port },
  { key: 'underlayPort', value: settings.value?.underlay.port },
  { key: 'clientsPort', value: settings.value?.clients.port },
  { key: 'storagePort', value: settings.value?.storage.port },
])

const hostPort = (host: string, port?: number) => (host.includes(':') ? `[${host}]` : host) + `:${port ?? ''}`
// Mirrors the hub: an empty storage endpoint means the client endpoint with the storage port.
const storageDefault = computed(() => (endpoint.value ? hostPort(endpoint.value, settings.value?.storage.port) : ''))

function onTabKeydown(e: KeyboardEvent) {
  const last = tabs.length - 1
  const i = tabs.indexOf(tab.value)
  const next = { ArrowDown: i === last ? 0 : i + 1, ArrowUp: i === 0 ? last : i - 1, Home: 0, End: last }[e.key]
  const target = next === undefined ? undefined : tabs[next]
  if (!target) return
  e.preventDefault()
  tab.value = target
  document.getElementById(`tab-${target}`)?.focus()
}

async function apply(forTab: Tab, call: () => Promise<Settings>) {
  error.value = ''
  try {
    const s = await call()
    if (forTab === 'networking') {
      endpoint.value = s.clients.endpoint
      storageEndpoint.value = s.storage.endpoint ?? ''
    }
    else if (forTab === 'security') token.value = s.token
    else mtu.value = s.guestMTU
    await refresh()
    savedTab.value = forTab
  } catch (e) {
    error.value = (e as Error).message
  }
}

const patch = (p: SettingsPatch) => () => api.updateSettings(p)

const devicesAffected = computed(() =>
  endpoint.value !== settings.value?.clients.endpoint ? (settings.value?.devices ?? 0) : 0,
)
const vmsAffected = computed(() =>
  (storageEndpoint.value || storageDefault.value) !== settings.value?.storageEndpoint ? (settings.value?.vmsWithDisks ?? 0) : 0,
)
const saveNetworking = () => api.updateSettings({ clients: { endpoint: endpoint.value }, storage: { endpoint: storageEndpoint.value } })

function saveEndpoint() {
  if (devicesAffected.value > 0 || vmsAffected.value > 0) pending.value = 'endpoint'
  else void apply('networking', saveNetworking)
}

function confirm() {
  const p = pending.value
  pending.value = null
  if (p === 'endpoint') void apply('networking', saveNetworking)
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
    ? {
        title: t('settings.endpointChangeTitle'),
        body: [
          devicesAffected.value > 0 ? t('settings.endpointChangeBody', { count: devicesAffected.value }, devicesAffected.value) : '',
          vmsAffected.value > 0 ? t('settings.storageChangeBody', { count: vmsAffected.value }, vmsAffected.value) : '',
        ]
          .filter(Boolean)
          .join(' '),
        label: t('settings.confirm'),
      }
    : { title: t('settings.tokenChangeTitle'), body: t('settings.tokenChangeBody'), label: t('settings.tokenChangeConfirm') },
)

watch(tab, () => {
  error.value = ''
  savedTab.value = null
})

// The images are built from the checkout's deploy/ files, so it must be on the release as well.
const upgradeCommand = computed(
  () => `git fetch --tags && git checkout ${updates.value?.latest ?? ''}\ndocker compose -f deploy/compose.yaml up -d --build`,
)
const checking = ref(false)
const copiedCommand = ref(false)
const checkedAgo = computed(() => {
  const at = updates.value?.checkedAt
  if (!at) return ''
  const minutes = Math.round((new Date(at).getTime() - Date.now()) / 60000)
  const rtf = new Intl.RelativeTimeFormat(locale.value, { numeric: 'auto' })
  return Math.abs(minutes) < 60 ? rtf.format(minutes, 'minute') : rtf.format(Math.round(minutes / 60), 'hour')
})

async function checkNow() {
  checking.value = true
  error.value = ''
  try {
    updates.value = await api.checkUpdates()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    checking.value = false
  }
}

async function copyCommand() {
  try {
    await navigator.clipboard.writeText(upgradeCommand.value)
    copiedCommand.value = true
    setTimeout(() => (copiedCommand.value = false), 1500)
  } catch {
    error.value = t('node.copyFailed')
  }
}
</script>

<template>
  <div>
    <PageHeader section="settings" />

    <div v-if="setupPending" role="alert" class="alert alert-warning alert-soft mb-4 text-sm">
      <AppIcon name="warning" />
      {{ t('settings.setupBanner') }}
    </div>

    <div v-if="loaded && !settings" role="alert" class="alert alert-error alert-soft text-sm">
      <span class="grow">{{ t('settings.loadFailed') }}</span>
      <button type="button" class="btn btn-sm" @click="refresh">{{ t('common.retry') }}</button>
    </div>
    <DataState v-else :loading="!settings">
      <div class="flex flex-col gap-6 md:flex-row">
        <ul class="menu menu-sm w-full gap-0.5 p-0 md:w-48 md:shrink-0" role="tablist" aria-orientation="vertical" @keydown="onTabKeydown">
          <li v-for="id in tabs" :key="id" role="presentation">
            <button
              :id="`tab-${id}`"
              type="button"
              role="tab"
              class="flex items-center gap-2"
              :class="tab === id ? 'menu-active' : ''"
              :aria-selected="tab === id"
              :tabindex="tab === id ? 0 : -1"
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
            <label class="form-control">
              <span class="label-text mb-1 text-sm font-medium">{{ t('settings.storageEndpoint') }}</span>
              <input v-model.trim="storageEndpoint" type="text" class="input input-sm input-bordered w-full font-token" :placeholder="storageDefault" />
              <span class="text-base-content/60 mt-1 text-xs">{{ t('settings.storageEndpointHint') }}</span>
            </label>
            <div class="grid grid-cols-2 gap-3">
              <label v-for="{ key, value } in ports" :key="key" class="form-control">
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
            v-else-if="tab === 'guests'"
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

          <div v-else-if="updates" class="flex flex-col gap-4 text-sm">
            <dl class="grid grid-cols-[auto_1fr] items-center gap-x-6 gap-y-2">
              <dt class="text-base-content/60">{{ t('settings.updates.hubVersion') }}</dt>
              <dd>
                <span class="font-token">{{ updates.version }}</span>
                <span class="text-base-content/60">
                  · {{ t('settings.updates.gateway') }} <span class="font-token">{{ updates.gateway || '—' }}</span> ·
                  {{ t('settings.updates.storage') }} <span class="font-token">{{ updates.storage || '—' }}</span>
                </span>
              </dd>
              <template v-if="updates.hubUpdate !== 'none'">
                <dt class="text-base-content/60">{{ t('settings.updates.latest') }}</dt>
                <dd class="flex flex-wrap items-center gap-3">
                  <span class="font-token">{{ updates.latest || '—' }}</span>
                  <span v-if="checkedAgo" class="text-base-content/60 text-xs">{{ t('settings.updates.checked', { ago: checkedAgo }) }}</span>
                  <button type="button" class="btn btn-sm" :disabled="checking" @click="checkNow">
                    <span v-if="checking" class="loading loading-spinner loading-xs"></span>
                    {{ t('settings.updates.checkNow') }}
                  </button>
                </dd>
              </template>
            </dl>
            <div v-if="updates.checkError" role="alert" class="alert alert-error alert-soft text-sm">
              {{ t('settings.updates.checkFailed', { error: updates.checkError }) }}
            </div>
            <p v-if="updates.hubUpdate === 'none'" class="text-base-content/60">{{ t('settings.updates.devBuild') }}</p>
            <template v-else-if="updates.updateAvailable">
              <p>{{ t('settings.updates.manual', { version: updates.latest }) }}</p>
              <pre class="bg-base-200 rounded-box font-token p-3 text-xs break-all whitespace-pre-wrap">{{ upgradeCommand }}</pre>
              <div>
                <button type="button" class="btn btn-sm" @click="copyCommand">
                  <AppIcon :name="copiedCommand ? 'check' : 'copy'" />
                  {{ copiedCommand ? t('vms.copied') : t('vms.copy') }}
                </button>
              </div>
            </template>
            <p v-else-if="updates.latest" class="text-base-content/60">{{ t('settings.updates.upToDate') }}</p>
          </div>
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
