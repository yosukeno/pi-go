<script setup lang="ts">
import type { StarterCard } from "@/api/types";

// Next-step chips at the tail of the conversation. Chips rather than cards on
// purpose: the empty state has the whole screen to introduce the agent, whereas
// this sits under an answer the user is still reading and must not compete with
// it. One line, no icons, no descriptions.
defineProps<{ chips: StarterCard[] }>();
const emit = defineEmits<{ pick: [StarterCard] }>();
</script>

<template>
  <div class="followups">
    <button
      v-for="(c, i) in chips"
      :key="`${c.title}-${i}`"
      class="chip"
      type="button"
      @click="emit('pick', c)"
    >
      {{ c.title }}
    </button>
  </div>
</template>

<style scoped lang="scss">
.followups {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 2px 0 10px;
}

.chip {
  padding: 4px 10px;
  font-size: 12px;
  color: var(--el-text-color-regular);
  background: var(--el-fill-color-lighter);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 999px;
  cursor: pointer;
  transition: border-color 0.15s ease, color 0.15s ease;

  &:hover {
    color: var(--el-color-primary);
    border-color: var(--el-color-primary);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}
</style>
