<script setup lang="ts">
import { computed } from "vue";
import MarkdownIt from "markdown-it";
import CodeBlock from "./CodeBlock.vue";
import MermaidDiagram from "./MermaidDiagram.vue";

// A fenced block is mounted as a component rather than left to markdown-it's
// <pre><code>: that is what gives an answer's code the same syntax colouring,
// language label and copy button as the file viewer, with the text still
// rendered as text nodes (never v-html).
//
// The source is therefore split into segments — runs of markdown rendered with
// v-html, and top-level fences rendered as components — instead of rendering one
// v-html blob and teleporting components into placeholder slots. A Teleport
// resolves its target once and only re-resolves when `to` changes, so with a
// single v-html blob every source update (i.e. every streamed token batch)
// replaced the placeholder divs and orphaned the already-mounted blocks: the new
// slots stayed empty and the diagram was never visible in the document.
interface Segment {
  id: string;
  kind: "html" | "mermaid" | "code";
  // Set for kind "html"; empty otherwise.
  html: string;
  // Set for the fence kinds; empty otherwise.
  source: string;
  lang: string;
}

type Token = ReturnType<InstanceType<typeof MarkdownIt>["parse"]>[number];

const props = defineProps<{ source: string; streaming?: boolean }>();

const md = new MarkdownIt({ html: false, linkify: true, breaks: true });

const segments = computed<Segment[]>(() => {
  const environment = {};
  const tokens = md.parse(props.source, environment);
  const out: Segment[] = [];
  let pending: Token[] = [];

  // Ids are positional so a growing answer keeps the same component instances:
  // appending text does not renumber the segments that precede it, which is what
  // lets a rendered diagram survive the next streamed batch.
  const flush = () => {
    if (pending.length === 0) return;
    out.push({
      id: `s${out.length}`,
      kind: "html",
      html: md.renderer.render(pending, md.options, environment),
      source: "",
      lang: "",
    });
    pending = [];
  };

  for (const token of tokens) {
    // Nested fences (inside a list item or quote) keep markdown-it's own
    // <pre><code>: pulling them out of the segment would drop their container.
    if (token.type !== "fence" || token.level !== 0) {
      pending.push(token);
      continue;
    }
    flush();
    const lang = token.info.trim().split(/\s+/, 1)[0]?.toLowerCase() ?? "";
    out.push({
      id: `s${out.length}`,
      kind: lang === "mermaid" ? "mermaid" : "code",
      html: "",
      source: token.content.replace(/\n$/, ""),
      lang,
    });
  }
  flush();
  return out;
});
</script>

<template>
  <div class="markdown-view">
    <template v-for="segment in segments" :key="segment.id">
      <!-- html:false escapes source HTML; fences never reach this branch. -->
      <div v-if="segment.kind === 'html'" class="markdown-content" v-html="segment.html" />

      <!-- A mermaid fence is almost certainly incomplete while the answer is still
           streaming, and MermaidDiagram re-renders asynchronously on every source
           change — so mid-stream it would thrash between "rendering…" and a parse
           error. Defer the diagram until the stream settles; show the source as
           code in the meantime. -->
      <CodeBlock
        v-else-if="segment.kind === 'mermaid' && streaming"
        class="block"
        :code="segment.source"
        lang="mermaid"
        :collapsed-height="420"
      />
      <MermaidDiagram v-else-if="segment.kind === 'mermaid'" class="block" :source="segment.source" />
      <CodeBlock
        v-else
        class="block"
        :code="segment.source"
        :lang="segment.lang"
        line-numbers
        :collapsed-height="420"
        :expanded-height="4000"
      />
    </template>
  </div>
</template>

<style scoped lang="scss">
.markdown-view > .block {
  min-width: 0;
  margin: 0.6em 0;
}
</style>
