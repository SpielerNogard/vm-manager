<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ label: string; used: number; total: number; format: (n: number) => string }>()

const percent = computed(() => (props.total ? Math.min(100, Math.round((props.used / props.total) * 100)) : 0))
</script>

<template>
  <div>
    <div class="mb-1 flex items-baseline justify-between gap-2 text-sm">
      <span class="font-medium">{{ label }}</span>
      <span class="text-base-content/65 tabular-nums">{{ format(used) }} / {{ format(total) }} · {{ percent }}%</span>
    </div>
    <progress
      class="progress w-full"
      :class="percent >= 90 ? 'progress-warning' : 'progress-primary'"
      :value="used"
      :max="total || 1"
      :aria-label="label"
    ></progress>
  </div>
</template>
