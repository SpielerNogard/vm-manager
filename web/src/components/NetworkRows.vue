<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { Network } from '@/api'

import AppIcon from './AppIcon.vue'

const props = defineProps<{ networks: Network[] }>()
const selected = defineModel<string[]>({ required: true })
const emit = defineEmits<{ manage: [] }>()

const { t } = useI18n()

const subnetOf = (id: string) => props.networks.find((n) => n.id === id)?.subnet ?? ''
const unused = computed(() => props.networks.filter((n) => !selected.value.includes(n.id)))
// Each row may switch to its own network or any not picked by another row.
const choices = (id: string) => props.networks.filter((n) => n.id === id || !selected.value.includes(n.id))

function add() {
  const next = unused.value[0]
  if (next) selected.value = [...selected.value, next.id]
}

function setAt(i: number, id: string) {
  selected.value = selected.value.map((cur, j) => (j === i ? id : cur))
}

function removeAt(i: number) {
  selected.value = selected.value.filter((_, j) => j !== i)
}
</script>

<template>
  <div
    v-if="!networks.length"
    class="border-base-300 rounded-box flex items-center justify-between gap-3 border border-dashed p-3 text-sm"
  >
    <span class="text-base-content/65">{{ t('networks.emptyTitle') }}</span>
    <button type="button" class="btn btn-sm" @click="emit('manage')">{{ t('networks.add') }}</button>
  </div>
  <template v-else>
    <div v-if="selected.length" class="text-base-content/60 mb-1 grid grid-cols-[1fr_1fr_2rem] gap-2 text-xs">
      <span>{{ t('create.network') }}</span><span>{{ t('networks.subnet') }}</span><span></span>
    </div>
    <div v-for="(id, i) in selected" :key="id" class="mb-2 grid grid-cols-[1fr_1fr_2rem] items-center gap-2">
      <select
        class="select select-sm w-full"
        :value="id"
        :aria-label="t('create.network')"
        @change="setAt(i, ($event.target as HTMLSelectElement).value)"
      >
        <option v-for="n in choices(id)" :key="n.id" :value="n.id">{{ n.name }}</option>
      </select>
      <span class="font-token text-base-content/55 text-xs">{{ subnetOf(id) }}</span>
      <button type="button" class="btn btn-ghost btn-sm btn-square" :title="t('create.removeNetwork')" @click="removeAt(i)">
        <AppIcon name="trash" />
        <span class="sr-only">{{ t('create.removeNetwork') }}</span>
      </button>
    </div>
    <button type="button" class="btn btn-sm btn-ghost" :disabled="!unused.length" @click="add">
      <AppIcon name="plus" />
      {{ t('networks.add') }}
    </button>
  </template>
</template>
