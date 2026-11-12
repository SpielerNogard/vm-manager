<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'

import { api, type VM } from '@/api'
import { useFleet } from '@/composables/useFleet'
import { formatMB, VM_TONE } from '@/lib/format'

import ActionsMenu from './ActionsMenu.vue'
import AppIcon from './AppIcon.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import StateBadge from './StateBadge.vue'

defineProps<{ vms: VM[] }>()

const { t } = useI18n()
const { refresh } = useFleet()

const pendingDelete = ref<VM | null>(null)
const pendingStop = ref<VM | null>(null)
const error = ref('')
const copiedId = ref('')

const mgmtHost = (vm: VM) => `${vm.name}.mgmt.internal`
const sshCommand = (vm: VM) => `ssh ubuntu@${mgmtHost(vm)}`

async function copy(vm: VM) {
  await navigator.clipboard?.writeText(sshCommand(vm))
  copiedId.value = vm.id
  setTimeout(() => {
    if (copiedId.value === vm.id) copiedId.value = ''
  }, 1600)
}

async function power(action: 'startVM' | 'shutdownVM' | 'stopVM', vm: VM) {
  error.value = ''
  try {
    await api[action](vm.id)
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  }
}

function stopVM() {
  const vm = pendingStop.value
  pendingStop.value = null
  if (vm) power('stopVM', vm)
}

async function deleteVM() {
  const vm = pendingDelete.value
  pendingDelete.value = null
  if (!vm) return
  error.value = ''
  try {
    await api.deleteVM(vm.id)
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  }
}
</script>

<template>
  <div v-if="error" role="alert" class="alert alert-error alert-soft m-2 text-sm">{{ error }}</div>

  <table class="table-sm table">
    <thead>
      <tr>
        <th>{{ t('vms.vm') }}</th>
        <th>{{ t('common.status') }}</th>
        <th>{{ t('vms.node') }}</th>
        <th class="text-right">{{ t('vms.resources') }}</th>
        <th>{{ t('vms.ip') }}</th>
        <th>{{ t('vms.mgmt') }}</th>
        <th class="w-0"></th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="vm in vms" :key="vm.id">
        <td>
          <RouterLink :to="{ name: 'vm', params: { id: vm.id } }" class="font-medium hover:underline">{{ vm.name }}</RouterLink>
          <div class="font-token text-base-content/55 text-[0.7rem]">{{ vm.id }}</div>
        </td>
        <td>
          <StateBadge :label="t(`state.${vm.state}`)" :tone="VM_TONE[vm.state] ?? 'quiet'" :reason="vm.error || (vm.state === 'pending' ? t('vms.waitingForNode') : undefined)" />
          <div v-if="vm.error" class="text-base-content/60 mt-0.5 max-w-52 text-xs">{{ vm.error }}</div>
        </td>
        <td class="text-sm">
          <RouterLink :to="{ name: 'node', params: { id: vm.nodeId } }" class="hover:underline">{{ vm.nodeId }}</RouterLink>
          <div class="font-token text-base-content/55 text-[0.7rem]">{{ vm.provider }}</div>
        </td>
        <td class="text-right text-sm tabular-nums">
          {{ vm.cpus }} vCPU · {{ formatMB(vm.memoryMB) }}<br />
          <span class="text-base-content/55 text-xs">{{ t('vms.root', { size: vm.diskGB }) }}</span>
          <div v-for="d in vm.attachments" :key="d.diskId" class="text-base-content/55 text-xs">
            <span class="font-token">{{ d.mountPath }}</span> · {{ d.sizeGB }} GB
          </div>
        </td>
        <td class="font-token text-sm">{{ vm.ip || '—' }}</td>
        <td>
          <div class="flex items-center gap-1">
            <div>
              <div class="font-token text-sm">{{ mgmtHost(vm) }}</div>
              <code class="font-token text-base-content/55 text-xs">{{ sshCommand(vm) }}</code>
            </div>
            <button
              type="button"
              class="btn btn-ghost btn-xs"
              :title="t(copiedId === vm.id ? 'vms.copied' : 'vms.copy')"
              @click="copy(vm)"
            >
              <AppIcon :name="copiedId === vm.id ? 'check' : 'copy'" />
              <span class="sr-only">{{ t('vms.copySsh') }}</span>
            </button>
          </div>
        </td>
        <td>
          <ActionsMenu :name="vm.name">
            <li v-if="vm.power === 'stopped' || vm.state === 'error'">
              <button type="button" @click="power('startVM', vm)">{{ t('vms.start') }}</button>
            </li>
            <template v-if="vm.power === 'running'">
              <li>
                <button type="button" @click="power('shutdownVM', vm)">{{ t('vms.shutdown') }}</button>
              </li>
              <li>
                <button type="button" @click="pendingStop = vm">{{ t('vms.stop') }}</button>
              </li>
            </template>
            <li>
              <button type="button" class="text-error" @click="pendingDelete = vm">{{ t('common.delete') }}</button>
            </li>
          </ActionsMenu>
        </td>
      </tr>
    </tbody>
  </table>

  <ConfirmDialog
    :open="pendingStop !== null"
    :title="t('vms.stopTitle', { name: pendingStop?.name })"
    :body="t('vms.stopBody')"
    :confirm-label="t('vms.stop')"
    @confirm="stopVM"
    @cancel="pendingStop = null"
  />

  <ConfirmDialog
    :open="pendingDelete !== null"
    :title="t('vms.deleteTitle', { name: pendingDelete?.name })"
    :body="t('vms.deleteBody')"
    :confirm-label="t('vms.deleteConfirm')"
    @confirm="deleteVM"
    @cancel="pendingDelete = null"
  />
</template>
