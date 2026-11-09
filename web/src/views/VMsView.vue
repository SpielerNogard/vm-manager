<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import CreateVMDialog from '@/components/CreateVMDialog.vue'
import DataState from '@/components/DataState.vue'
import PageHeader from '@/components/PageHeader.vue'
import VMTable from '@/components/VMTable.vue'
import { useFleet } from '@/composables/useFleet'

const { t } = useI18n()
const { vms, onlineNodes, loaded } = useFleet()

const createOpen = ref(false)
</script>

<template>
  <div>
    <PageHeader section="vms">
      <template #actions>
        <button type="button" class="btn btn-sm btn-primary" :disabled="!onlineNodes.length" @click="createOpen = true">
          <AppIcon name="plus" />
          {{ t('vms.new') }}
        </button>
      </template>
    </PageHeader>

    <DataState :loading="!loaded" :empty="!vms.length" :empty-title="t('vms.emptyTitle')">
      <template #empty-body>{{ t('vms.emptyBody') }}</template>
      <template #empty-action>
        <button type="button" class="btn btn-sm btn-primary" :disabled="!onlineNodes.length" @click="createOpen = true">
          <AppIcon name="plus" />
          {{ t('vms.new') }}
        </button>
      </template>

      <VMTable :vms="vms" />
    </DataState>

    <CreateVMDialog v-model:open="createOpen" />
  </div>
</template>
