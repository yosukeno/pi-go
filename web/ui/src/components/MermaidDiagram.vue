<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Check, CopyDocument, Download, FullScreen } from "@element-plus/icons-vue";
import CodeBlock from "./CodeBlock.vue";
import { renderMermaid } from "./mermaidRuntime";

const MAX_SOURCE_BYTES = 64 * 1024;
const MAX_SOURCE_LINES = 1_000;
// Fills darker than this get lightened: a diagram's own `style fill:#1f2937` is
// authored for slide decks, not for a light chat surface. The mix keeps enough
// saturation for the block to still read as a colour, not as a tint.
const DARK_FILL_LUMINANCE = 0.55;
const LIGHTEN_TOWARDS_WHITE = 0.42;
const BORDER_DARKEN = 0.25;

const props = defineProps<{ source: string }>();

const { t } = useI18n();
const svg = ref("");
const error = ref("");
const canvas = ref<HTMLElement | null>(null);
const shell = ref<HTMLElement | null>(null);
const copied = ref(false);
const notice = ref("");
const fullscreen = ref(false);
let generation = 0;
let copyTimer: number | undefined;

function readRGB(color: string): [number, number, number] | null {
  const match = color.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/i);
  if (!match) return null;
  return [Number(match[1]), Number(match[2]), Number(match[3])];
}

function luminanceOf([red, green, blue]: [number, number, number]): number {
  return (0.2126 * red + 0.7152 * green + 0.0722 * blue) / 255;
}

function lighten(rgb: [number, number, number], amount: number): [number, number, number] {
  return rgb.map((channel) => Math.round(channel + (255 - channel) * amount)) as [number, number, number];
}

function darken(rgb: [number, number, number], amount: number): [number, number, number] {
  return rgb.map((channel) => Math.round(channel * (1 - amount))) as [number, number, number];
}

function toCSS([red, green, blue]: [number, number, number]): string {
  return `rgb(${red}, ${green}, ${blue})`;
}

/**
 * Diagram-authored colours are respected in hue but not in darkness: a dark fill is
 * mixed towards white and its border kept slightly darker, which keeps the shape
 * readable without the heavy blocks the model tends to emit.
 */
function softenPalette() {
  const nodes = canvas.value?.querySelectorAll<SVGGElement>("svg g.node") ?? [];
  for (const node of nodes) {
    const shapes = node.querySelectorAll<SVGGraphicsElement>("rect, circle, ellipse, polygon, path");
    let background: [number, number, number] | null = null;
    for (const shape of shapes) {
      const fill = readRGB(getComputedStyle(shape).fill);
      if (!fill) continue;
      const softened = luminanceOf(fill) < DARK_FILL_LUMINANCE ? lighten(fill, LIGHTEN_TOWARDS_WHITE) : fill;
      if (softened !== fill) {
        shape.style.fill = toCSS(softened);
        shape.style.stroke = toCSS(darken(softened, BORDER_DARKEN));
      }
      background ??= softened;
    }

    // Text colour is decided from the fill that actually ends up on screen, so a
    // mid-tone block gets white text instead of unreadable dark-on-dark.
    const color = background && luminanceOf(background) < 0.58 ? "#ffffff" : "#111827";
    for (const text of node.querySelectorAll<SVGTextElement>("text")) {
      text.style.fill = color;
      for (const span of text.querySelectorAll<SVGTSpanElement>("tspan")) {
        span.style.fill = color;
      }
    }
  }
}

async function render(source: string) {
  const current = ++generation;
  svg.value = "";
  error.value = "";
  notice.value = "";

  const bytes = new TextEncoder().encode(source).byteLength;
  if (bytes > MAX_SOURCE_BYTES || source.split("\n").length > MAX_SOURCE_LINES) {
    error.value = t("mermaidDiagram.tooLarge");
    return;
  }

  try {
    const safeSVG = await renderMermaid(source);
    if (current !== generation) return;
    svg.value = safeSVG;
    await nextTick();
    if (current === generation) softenPalette();
  } catch (cause) {
    if (current !== generation) return;
    error.value = cause instanceof Error ? cause.message : t("mermaidDiagram.failed");
  }
}

async function copySource() {
  try {
    await navigator.clipboard.writeText(props.source);
    copied.value = true;
    window.clearTimeout(copyTimer);
    copyTimer = window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard access can be denied; failing quietly is better than an alert.
  }
}

/**
 * Export goes through a serialized copy of the rendered SVG. Labels are SVG text and
 * the markup carries no external references, so a canvas draw is enough — the
 * foreignObject case that breaks canvas export cannot occur here.
 */
async function downloadImage() {
  const source = canvas.value?.querySelector("svg");
  if (!source) return;
  notice.value = "";

  const box = source.getBoundingClientRect();
  const width = Math.max(Math.ceil(box.width || source.viewBox.baseVal.width || 800), 1);
  const height = Math.max(Math.ceil(box.height || source.viewBox.baseVal.height || 600), 1);

  const clone = source.cloneNode(true) as SVGSVGElement;
  clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
  clone.setAttribute("width", String(width));
  clone.setAttribute("height", String(height));

  const markup = new XMLSerializer().serializeToString(clone);
  const svgURL = URL.createObjectURL(new Blob([markup], { type: "image/svg+xml;charset=utf-8" }));
  try {
    const image = new Image();
    image.decoding = "sync";
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve();
      image.onerror = () => reject(new Error("svg load failed"));
      image.src = svgURL;
    });

    // 2x keeps the text crisp when the PNG is pasted into a document.
    const scale = 2;
    const target = document.createElement("canvas");
    target.width = width * scale;
    target.height = height * scale;
    const context = target.getContext("2d");
    if (!context) throw new Error("canvas unavailable");
    context.fillStyle = "#ffffff";
    context.fillRect(0, 0, target.width, target.height);
    context.setTransform(scale, 0, 0, scale, 0, 0);
    context.drawImage(image, 0, 0);

    const blob = await new Promise<Blob | null>((resolve) => target.toBlob(resolve, "image/png"));
    if (!blob) throw new Error("encode failed");

    const pngURL = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = pngURL;
    link.download = `mermaid-${new Date().toISOString().replace(/[:.]/g, "-")}.png`;
    link.click();
    window.setTimeout(() => URL.revokeObjectURL(pngURL), 1000);
  } catch {
    notice.value = t("mermaidDiagram.downloadFailed");
  } finally {
    URL.revokeObjectURL(svgURL);
  }
}

/**
 * Native fullscreen when the browser allows it, with an in-page overlay fallback so
 * the button always does something (permissions policy can block the API in frames).
 */
async function toggleFullscreen() {
  const element = shell.value;
  if (!element) return;
  try {
    if (document.fullscreenElement) {
      await document.exitFullscreen();
    } else if (element.requestFullscreen) {
      await element.requestFullscreen();
    } else {
      fullscreen.value = !fullscreen.value;
    }
  } catch {
    fullscreen.value = !fullscreen.value;
  }
}

function syncFullscreen() {
  fullscreen.value = document.fullscreenElement === shell.value;
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === "Escape" && fullscreen.value && !document.fullscreenElement) {
    fullscreen.value = false;
  }
}

watch(() => props.source, render, { immediate: true });
onMounted(() => {
  document.addEventListener("fullscreenchange", syncFullscreen);
  window.addEventListener("keydown", onKeydown);
});
onBeforeUnmount(() => {
  generation++;
  window.clearTimeout(copyTimer);
  document.removeEventListener("fullscreenchange", syncFullscreen);
  window.removeEventListener("keydown", onKeydown);
});
</script>

<template>
  <div ref="shell" class="mermaid-diagram" :class="{ 'is-fullscreen': fullscreen }">
    <div v-if="svg" class="bar">
      <button class="act" @click="copySource">
        <el-icon><Check v-if="copied" /><CopyDocument v-else /></el-icon>
        <span>{{ copied ? t("common.copied") : t("mermaidDiagram.copyCode") }}</span>
      </button>
      <button class="act" @click="downloadImage">
        <el-icon><Download /></el-icon>
        <span>{{ t("mermaidDiagram.downloadImage") }}</span>
      </button>
      <button class="act" @click="toggleFullscreen">
        <el-icon><FullScreen /></el-icon>
        <span>{{ fullscreen ? t("mermaidDiagram.exitFullscreen") : t("mermaidDiagram.fullscreen") }}</span>
      </button>
    </div>
    <div v-if="notice" class="note">{{ notice }}</div>
    <!-- The SVG is produced in strict mode and sanitized before insertion. -->
    <div v-if="svg" ref="canvas" class="canvas" v-html="svg" />
    <div v-else-if="error" class="fallback">
      <div class="note bad">Mermaid: {{ error }}</div>
      <CodeBlock :code="source" lang="mermaid" :collapsed-height="240" />
    </div>
    <div v-else class="note">{{ t("mermaidDiagram.rendering") }}</div>
  </div>
</template>

<style scoped lang="scss">
.mermaid-diagram {
  position: relative;
  margin: 0.7em 0;
  min-width: 0;

  /* Fullscreen: the native backdrop is black, so the surface is painted here and
     the canvas is allowed to use the whole viewport. The same class drives the
     in-page fallback overlay. */
  &.is-fullscreen {
    position: fixed;
    inset: 0;
    z-index: 3000;
    margin: 0;
    display: flex;
    flex-direction: column;
    background: var(--el-bg-color);
    padding: 44px 16px 16px;

    .canvas {
      flex: 1;
      min-height: 0;
      border: 0;
      display: flex;
      align-items: center;
      justify-content: center;
    }

    :deep(svg) {
      max-width: 100%;
      max-height: 100%;
    }
  }

  &:fullscreen {
    background: var(--el-bg-color);
    padding: 44px 16px 16px;
  }
}

.bar {
  position: absolute;
  top: 8px;
  right: 10px;
  z-index: 1;
  display: flex;
  gap: 6px;
}

/* Against a white page a white button is invisible: these sit on the grey fill
   ramp, one step darker on hover. */
.act {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--el-border-color);
  border-radius: 5px;
  background: var(--el-fill-color-dark);
  color: var(--el-text-color-regular);
  font-size: 11px;
  line-height: 1.6;
  padding: 3px 9px;
  cursor: pointer;

  .el-icon {
    font-size: 12px;
  }

  &:hover {
    background: var(--el-fill-color-darker);
    border-color: var(--el-border-color-dark);
    color: var(--el-text-color-primary);
  }

  &:active {
    background: var(--el-border-color-light);
  }
}

.canvas {
  overflow: auto;
  padding: 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  background: var(--el-bg-color);

  :deep(svg) {
    display: block;
    width: auto;
    min-width: 420px;
    max-width: none;
    height: auto;
    margin: 0 auto;
  }
}

.note {
  margin-bottom: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);

  &.bad {
    color: var(--el-color-danger);
  }
}
</style>
