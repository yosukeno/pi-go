<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { THEMES, applyTheme, currentThemeId } from "@/theme";

/**
 * The skin switcher, beside the language control.
 *
 * Next to the locale rather than in the topbar deliberately: both are preferences
 * about the window rather than facts about the session or this run, and a person
 * looking for one of them looks in the same place for the other.
 *
 * Rows are swatches, not names. "Nord" means nothing until you have seen it, and a
 * list of fourteen proper nouns is a quiz; three chips per row — chrome, canvas,
 * accent — is the actual answer to what a skin looks like, in the order the
 * interface stacks them.
 */
const props = defineProps<{
  /** placement of the popover, so the collapsed rail can open it sideways. */
  placement?: "top-start" | "right-start";
  /** icon says drop the label and render as a square rail button. */
  icon?: boolean;
}>();

const { t } = useI18n();

const open = ref(false);

const light = computed(() => THEMES.filter((x) => x.mode === "light"));
const dark = computed(() => THEMES.filter((x) => x.mode === "dark"));
const current = computed(() => THEMES.find((x) => x.id === currentThemeId.value) ?? THEMES[0]);

function pick(id: string) {
  open.value = false;
  if (id !== currentThemeId.value) applyTheme(id);
}
</script>

<template>
  <el-popover
    v-model:visible="open"
    :placement="props.placement ?? 'top-start'"
    trigger="click"
    :width="248"
    :teleported="false"
    popper-class="theme-pop"
  >
    <template #reference>
      <button class="theme-btn" :class="{ iconic: props.icon }" :title="t('theme.label')">
        <!-- A live swatch as the trigger: the button shows the skin it would let
             you change, which is more use than a paint-roller glyph. -->
        <span class="swatch" aria-hidden="true">
          <i :style="{ background: current.page }" />
          <i :style="{ background: current.canvas }" />
          <i :style="{ background: current.accent }" />
        </span>
        <span v-if="!props.icon" class="btn-name">{{ current.name }}</span>
      </button>
    </template>

    <div class="list">
      <div class="group">{{ t("theme.light") }}</div>
      <button v-for="x in light" :key="x.id" class="item" :class="{ on: x.id === currentThemeId }" @click="pick(x.id)">
        <span class="swatch big" aria-hidden="true">
          <i :style="{ background: x.page }" />
          <i :style="{ background: x.canvas }" />
          <i :style="{ background: x.accent }" />
        </span>
        <span class="text">
          <span class="name">{{ x.name }}</span>
          <span class="origin">{{ x.origin }}</span>
        </span>
        <span v-if="x.id === currentThemeId" class="tick">✓</span>
      </button>

      <div class="group">{{ t("theme.dark") }}</div>
      <button v-for="x in dark" :key="x.id" class="item" :class="{ on: x.id === currentThemeId }" @click="pick(x.id)">
        <span class="swatch big" aria-hidden="true">
          <i :style="{ background: x.page }" />
          <i :style="{ background: x.canvas }" />
          <i :style="{ background: x.accent }" />
        </span>
        <span class="text">
          <span class="name">{{ x.name }}</span>
          <span class="origin">{{ x.origin }}</span>
        </span>
        <span v-if="x.id === currentThemeId" class="tick">✓</span>
      </button>
    </div>
  </el-popover>
</template>

<style scoped lang="scss">
.theme-btn {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 6px 8px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--el-text-color-regular);
  font-size: 12px;
  cursor: pointer;
  transition: background var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
  }

  &.iconic {
    justify-content: center;
    width: 36px;
    height: 36px;
    padding: 0;
    border-radius: 50%;
  }
}

.btn-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Three chips in the order the interface stacks them: chrome, content, accent.
   The ring keeps a white canvas chip visible on a light skin, where it would
   otherwise disappear into the row. */
.swatch {
  flex: 0 0 auto;
  display: inline-flex;
  border-radius: 999px;
  overflow: hidden;
  box-shadow: inset 0 0 0 1px var(--el-border-color);

  i {
    display: block;
    width: 6px;
    height: 14px;
  }

  &.big i {
    width: 8px;
    height: 20px;
  }
}

:deep(.theme-pop) {
  padding: 6px !important;
  border-radius: 12px !important;
}

.list {
  display: flex;
  flex-direction: column;
  gap: 1px;
  max-height: 60vh;
  overflow-y: auto;
}

.group {
  padding: 6px 8px 3px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--el-text-color-placeholder);
}

.item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 6px 8px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
  transition: background var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
  }

  &.on {
    background: var(--pg-accent-wash);
  }
}

.text {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.name {
  font-size: 12.5px;
  font-weight: 550;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Attribution, not decoration: these palettes are other people's work and the
   picker is where saying so costs nothing. */
.origin {
  font-size: 10px;
  color: var(--el-text-color-secondary);
}

.tick {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--el-color-primary);
}
</style>
