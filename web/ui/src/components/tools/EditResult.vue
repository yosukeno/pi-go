<script setup lang="ts">
import { useI18n } from "vue-i18n";
import DiffView from "../DiffView.vue";
import type { EditDetails } from "@/api/types";

// The reason the whole structured-details channel exists: for a coding agent the
// diff is the most important thing on screen, and it deliberately never enters
// the model's context.
const props = defineProps<{ details: EditDetails }>();

const { t } = useI18n();
</script>

<template>
  <div class="edit">
    <DiffView :diff="details.diff" :path="details.path" :added="details.added" :removed="details.removed" />
    <div v-if="details.edits > 1" class="meta">{{ t("editResult.edits", { n: details.edits }) }}</div>
  </div>
</template>

<style scoped lang="scss">
.meta {
  margin-top: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
</style>
