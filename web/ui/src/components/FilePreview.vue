<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import MarkdownIt from "markdown-it";
import { ArrowLeft, Check, CopyDocument, EditPen } from "@element-plus/icons-vue";
import CodeBlock from "./CodeBlock.vue";
import { api, fileRawURL } from "@/api/client";
import type { FileContent } from "@/api/types";

// Whole-panel file viewer: breadcrumb back to the tree, then the content in
// the best shape the file allows — markdown rendered, images inline, code
// highlighted, anything binary politely declined.
const props = defineProps<{ path: string }>();
const emit = defineEmits<{ back: [] }>();

// html:false plus markdown-it's escaping, the same combination TurnCard uses
// for assistant text: a workspace file can contain anything.
const md = new MarkdownIt({ html: false, linkify: true, breaks: true });

const content = ref<FileContent | null>(null);
const failed = ref("");
const mdPreview = ref(true);
const copied = ref(false);

// In-panel editing. Two guards: only files read in full (a truncated preview
// saved back would silently amputate the tail), and an mtime check so an
// outside change is never overwritten blind — the server 409s, and the user
// picks override or discard.
const editing = ref(false);
const draft = ref("");
const saving = ref(false);
const savedOk = ref(false);
const conflict = ref(false);
const saveError = ref("");

const ext = computed(() => {
  const base = props.path.split("/").pop() ?? "";
  const i = base.lastIndexOf(".");
  return i > 0 ? base.slice(i + 1).toLowerCase() : "";
});

const isMd = computed(() => ext.value === "md" || ext.value === "markdown");

const kind = computed(() => {
  const c = content.value;
  if (!c) return "loading";
  if (c.binary) return c.mime?.startsWith("image/") ? "image" : "binary";
  return "text";
});

async function load() {
  content.value = null;
  failed.value = "";
  try {
    content.value = await api.fileContent(props.path);
  } catch (e) {
    failed.value = e instanceof Error ? e.message : String(e);
  }
}
watch(() => props.path, load, { immediate: true });

const renderedMd = computed(() => (content.value?.text ? md.render(content.value.text) : ""));

function fmtSize(n?: number): string {
  if (n === undefined) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

const { t, d } = useI18n();

function fmtTime(ms?: number): string {
  if (!ms) return "";
  return d(new Date(ms), "short");
}

async function copyPath() {
  try {
    await navigator.clipboard.writeText(props.path);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard access can be denied; failing quietly is better than an alert.
  }
}

const canEdit = computed(() => kind.value === "text" && content.value && !content.value.truncated);

function startEdit() {
  draft.value = content.value?.text ?? "";
  conflict.value = false;
  saveError.value = "";
  editing.value = true;
}

async function save(force = false) {
  saving.value = true;
  saveError.value = "";
  try {
    const res = await api.saveFile(props.path, draft.value, content.value?.mtime_ms ?? 0, force);
    content.value = { ...content.value!, text: draft.value, size: res.size, mtime_ms: res.mtime_ms };
    editing.value = false;
    conflict.value = false;
    savedOk.value = true;
    window.setTimeout(() => (savedOk.value = false), 1500);
  } catch (e) {
    const status = (e as { status?: number }).status;
    if (status === 409) conflict.value = true;
    else saveError.value = e instanceof Error ? e.message : String(e);
  } finally {
    saving.value = false;
  }
}

// discard reloads from disk: the user picked the file's version over theirs.
async function discard() {
  editing.value = false;
  conflict.value = false;
  await load();
}
</script>

<template>
  <div class="preview">
    <div class="bar">
      <button class="ghost back" @click="emit('back')">
        <el-icon><ArrowLeft /></el-icon> {{ t("filePreview.back") }}
      </button>
      <span class="path" :title="path">{{ path }}</span>
      <span v-if="savedOk" class="saved">{{ t("filePreview.saved") }}</span>
      <button v-if="canEdit && !editing" class="ghost" :title="t('filePreview.edit')" @click="startEdit">
        <el-icon><EditPen /></el-icon>
      </button>
      <button class="ghost" :title="t('filePreview.copyPath')" @click="copyPath">
        <el-icon><Check v-if="copied" /><CopyDocument v-else /></el-icon>
      </button>
    </div>

    <div v-if="failed" class="note bad">{{ failed }}</div>
    <div v-else-if="!content" class="note">{{ t("common.loading") }}</div>

    <!-- The user's own draft. Saving checks the file's mtime against the one
         this preview read; a 409 offers override or discard, never a blind
         overwrite. -->
    <template v-if="editing">
      <div v-if="conflict" class="conflict">
        {{ t("filePreview.conflict") }}
        <button class="link danger" :disabled="saving" @click="save(true)">{{ t("filePreview.overwrite") }}</button>
        <button class="link" @click="discard">{{ t("filePreview.discard") }}</button>
      </div>
      <div v-if="saveError" class="note bad">{{ saveError }}</div>
      <textarea v-model="draft" class="editor" spellcheck="false" />
      <div class="editbar">
        <button class="act primary" :disabled="saving" @click="save(false)">
          {{ saving ? t("filePreview.saving") : t("common.save") }}
        </button>
        <button class="act" @click="discard">{{ t("common.cancel") }}</button>
      </div>
    </template>

    <template v-else-if="content">
      <div class="meta">
        <span>{{ fmtSize(content.size) }}</span>
        <span v-if="content.mtime_ms">{{ fmtTime(content.mtime_ms) }}</span>
        <span v-if="content.truncated" class="warn">
          {{ content.truncated_by === "lines" ? t("filePreview.truncatedLines") : t("filePreview.truncatedSize") }}
        </span>
        <span class="spacer" />
        <template v-if="kind === 'text' && isMd">
          <button class="mode" :class="{ on: mdPreview }" @click="mdPreview = true">{{ t("filePreview.preview") }}</button>
          <button class="mode" :class="{ on: !mdPreview }" @click="mdPreview = false">{{ t("filePreview.source") }}</button>
        </template>
      </div>

      <div class="body">
        <div v-if="kind === 'image'" class="image-wrap">
          <img :src="fileRawURL(path)" :alt="path" />
        </div>
        <div v-else-if="kind === 'binary'" class="note">
          {{ t("filePreview.binaryNoPreview", { mime: content.mime ?? "" }) }}
        </div>
        <!-- eslint-disable-next-line vue/no-v-html -- escaped by markdown-it -->
        <div v-else-if="isMd && mdPreview" class="md" v-html="renderedMd" />
        <CodeBlock
          v-else
          :code="content.text ?? ''"
          :lang="ext"
          line-numbers
          :collapsed-height="520"
          :expanded-height="100000"
        />
      </div>
    </template>
  </div>
</template>

<style scoped lang="scss">
.preview {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.bar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.path {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  color: var(--el-text-color-regular);
  direction: rtl; /* long paths clip on the left, keeping the file name visible */
  text-align: left;
}

.ghost {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 0;
  background: transparent;
  padding: 4px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  flex: 0 0 auto;

  &:hover {
    background: var(--el-fill-color);
  }
}

.meta {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  border-bottom: 1px solid var(--el-border-color-lighter);

  .warn {
    color: var(--el-color-warning);
  }

  .spacer {
    flex: 1;
  }
}

.mode {
  border: 0;
  background: transparent;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &.on {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

.body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 8px;
}

.note {
  padding: 16px 12px;
  font-size: 12px;
  color: var(--el-text-color-secondary);

  &.bad {
    color: var(--el-color-danger);
  }
}

.saved {
  font-size: 11px;
  color: var(--el-color-success);
  flex: 0 0 auto;
}

.editor {
  flex: 1;
  min-height: 0;
  border: 0;
  resize: none;
  padding: 10px 12px;
  font: 12px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--el-text-color-primary);
  background: var(--el-bg-color);
  outline: none;
  tab-size: 4;
}

.editbar {
  display: flex;
  gap: 8px;
  padding: 8px 10px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.act {
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  padding: 4px 14px;
  border-radius: 6px;
  font-size: 12px;
  color: var(--el-text-color-regular);
  cursor: pointer;

  &.primary {
    background: #000;
    border-color: #000;
    color: #fff;
  }

  &:disabled {
    opacity: 0.5;
    cursor: default;
  }
}

.conflict {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--el-color-warning);
  background: color-mix(in srgb, var(--el-color-warning) 8%, transparent);
  border-bottom: 1px solid var(--el-border-color-lighter);

  .link {
    border: 0;
    background: transparent;
    cursor: pointer;
    font-size: 12px;
    text-decoration: underline;
    color: var(--el-text-color-primary);
    padding: 0 2px;

    &.danger {
      color: var(--el-color-danger);
    }
  }
}

.image-wrap {
  display: flex;
  justify-content: center;

  img {
    max-width: 100%;
    border-radius: 6px;
  }
}

/* Mirrors TurnCard's markdown rules, tables included — the UA stylesheet draws
   no table borders, so a GFM table would read as aligned text without them. */
.md {
  font-size: 13px;
  line-height: 1.7;

  :deep(p) {
    margin: 0.4em 0;
  }

  :deep(pre) {
    background: var(--el-fill-color-light);
    padding: 8px 10px;
    border-radius: 6px;
    overflow-x: auto;
    font: 12px/1.55 ui-monospace, monospace;
  }

  :deep(code) {
    font-family: ui-monospace, monospace;
    font-size: 0.92em;
  }

  :deep(ul),
  :deep(ol) {
    padding-left: 1.4em;
    margin: 0.4em 0;
  }

  :deep(table) {
    border-collapse: collapse;
    margin: 0.5em 0;
    font-size: 13px;
    display: block;
    width: max-content;
    max-width: 100%;
    overflow-x: auto;
  }

  :deep(th),
  :deep(td) {
    border: 1px solid var(--el-border-color);
    padding: 4px 10px;
    text-align: left;
  }

  :deep(th) {
    background: var(--el-fill-color-light);
  }
}
</style>
