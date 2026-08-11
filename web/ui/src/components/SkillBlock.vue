<script setup lang="ts">
import { ref } from "vue";
import type { SkillBlock } from "@/agent/timeline";

defineProps<{ block: SkillBlock }>();

// Collapsed by default. A skill body is often hundreds of lines; expanded, it
// would push the question the user asked off the screen.
const open = ref(false);
</script>

<template>
  <div class="skill">
    <button class="head" @click="open = !open">
      <span class="caret">{{ open ? "▾" : "▸" }}</span>
      <span class="tag">skill</span>
      <span class="name">{{ block.name }}</span>
      <span class="path" :title="block.location">{{ block.location }}</span>
    </button>
    <pre v-if="open" class="body">{{ block.body }}</pre>
    <!-- What the user actually typed after the command stays visible either way:
         it is the request, and the skill is only the context for it. -->
    <div v-if="block.trailing" class="trailing">{{ block.trailing }}</div>
  </div>
</template>

<style scoped lang="scss">
.skill {
  margin: 2px 0;
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
  padding: 5px 8px;
  cursor: pointer;
  font-size: 12px;
  text-align: left;
}

.caret {
  color: var(--el-text-color-secondary);
}

.tag {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 3px;
  background: color-mix(in srgb, var(--el-color-success) 18%, transparent);
  color: var(--el-color-success);
}

.name {
  font-family: ui-monospace, monospace;
  font-weight: 600;
}

.path {
  margin-left: auto;
  color: var(--el-text-color-secondary);
  font-family: ui-monospace, monospace;
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  direction: rtl;
}

.body {
  margin: 6px 0 0;
  padding: 8px 10px;
  max-height: 320px;
  overflow: auto;
  background: var(--el-fill-color-light);
  border-radius: 6px;
  font: 12px/1.55 ui-monospace, monospace;
  white-space: pre-wrap;
}

.trailing {
  margin-top: 6px;
  font-size: 14px;
  line-height: 1.7;
  white-space: pre-wrap;
}
</style>
