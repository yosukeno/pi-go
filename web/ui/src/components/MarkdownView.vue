<script setup lang="ts">
import { computed, useId } from "vue";
import MarkdownIt from "markdown-it";
import CodeBlock from "./CodeBlock.vue";
import MermaidDiagram from "./MermaidDiagram.vue";

// A fenced block is mounted as a component rather than left to markdown-it's
// <pre><code>: that is what gives an answer's code the same syntax colouring,
// language label and copy button as the file viewer, with the text still
// rendered as text nodes (never v-html).
interface Block {
  id: string;
  kind: "mermaid" | "code";
  source: string;
  lang: string;
}

interface RenderEnvironment {
  blocks: Block[];
  instanceId: string;
}

const props = defineProps<{ source: string; streaming?: boolean }>();

const instanceId = useId().replace(/[^a-zA-Z0-9_-]/g, "-");
const md = new MarkdownIt({ html: false, linkify: true, breaks: true });

md.renderer.rules.fence = (tokens, index, _options, env) => {
  const token = tokens[index];
  const lang = token.info.trim().split(/\s+/, 1)[0]?.toLowerCase() ?? "";
  const context = env as unknown as RenderEnvironment;
  const id = `pi-md-${context.instanceId}-${context.blocks.length}`;
  context.blocks.push({
    id,
    kind: lang === "mermaid" ? "mermaid" : "code",
    source: token.content.replace(/\n$/, ""),
    lang,
  });
  return `<div id="${id}" class="block-slot"></div>\n`;
};

const rendered = computed(() => {
  const blocks: Block[] = [];
  const html = md.render(props.source, { blocks, instanceId });
  return { blocks, html };
});
</script>

<template>
  <div class="markdown-view">
    <!-- html:false escapes source HTML; only controlled slot IDs are inserted. -->
    <div class="markdown-content" v-html="rendered.html" />
    <Teleport v-for="block in rendered.blocks" :key="block.id" defer :to="`#${block.id}`">
      <!-- A mermaid fence is almost certainly incomplete while the answer is still
           streaming, and MermaidDiagram re-renders asynchronously on every source
           change — so mid-stream it would thrash between "rendering…" and a parse
           error. Defer the diagram until the stream settles; show the source as
           code in the meantime. -->
      <CodeBlock
        v-if="block.kind === 'mermaid' && streaming"
        :code="block.source"
        lang="mermaid"
        :collapsed-height="420"
      />
      <MermaidDiagram v-else-if="block.kind === 'mermaid'" :source="block.source" />
      <CodeBlock
        v-else
        :code="block.source"
        :lang="block.lang"
        line-numbers
        :collapsed-height="420"
        :expanded-height="4000"
      />
    </Teleport>
  </div>
</template>

<style scoped lang="scss">
.markdown-content :deep(.block-slot) {
  min-width: 0;
  margin: 0.6em 0;
}
</style>
