<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { ElConfigProvider } from "element-plus";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import en from "element-plus/es/locale/lang/en";
import ja from "element-plus/es/locale/lang/ja";
import ko from "element-plus/es/locale/lang/ko";
import AgentView from "@/views/AgentView.vue";

// Element Plus's own built-in copy (dialog buttons, popconfirm, ...) follows
// the app locale; our own strings go through vue-i18n message files.
const { locale } = useI18n();
const elLocales = { "zh-CN": zhCn, en, ja, ko } as const;
const elLocale = computed(
  () => elLocales[locale.value as keyof typeof elLocales] ?? zhCn,
);
</script>

<template>
  <el-config-provider :locale="elLocale">
    <AgentView />
  </el-config-provider>
</template>
