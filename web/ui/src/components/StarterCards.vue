<script setup lang="ts">
import { Icon } from "@iconify/vue";
import type { StarterCard } from "@/api/types";

// The empty conversation, which is the one screen where a general-purpose agent
// looks identical to every other one. A blank input signals infinite
// possibility and zero direction at the same time; a handful of concrete
// openings is what turns "ask me anything" into "here is what I am for".
//
// The content is the deployment's (a skill's starters.json), never pi-go's: this
// component knows how to draw a card and nothing about what the cards say.
const props = defineProps<{
  heading?: string;
  cards: StarterCard[];
  fallback: string;
}>();

const emit = defineEmits<{ pick: [StarterCard] }>();

// Icon names are intents, not pictures, so the set can be restyled without every
// deployment's file going stale. The server validates against the same names;
// anything unknown draws nothing rather than breaking the row.
const paths: Record<string, string> = {
  search: "M10.5 10.5 14 14M12 7.5a4.5 4.5 0 1 1-9 0 4.5 4.5 0 0 1 9 0Z",
  code: "m5.5 4.5-3 3 3 3m5-6 3 3-3 3",
  shield: "M8 2 3 4v4c0 2.5 2 4.3 5 5 3-.7 5-2.5 5-5V4L8 2Z",
  graph: "M4 11.5V7m4 4.5v-8m4 8V9M2.5 13.5h11",
  file: "M4 2.5h5l3 3v8H4v-11ZM9 2.5v3h3",
  terminal: "m4 6 2 2-2 2m4 0h4M2.5 3.5h10v9h-10v-9Z",
  spark: "M8 2.5l1.3 3.2 3.2 1.3-3.2 1.3L8 11.5 6.7 8.3 3.5 7l3.2-1.3L8 2.5Z",
  book: "M3 3.5h4.5V13H3v-9.5Zm5.5 0H13V13H8.5v-9.5Z",
};

function iconFor(name?: string) {
  const d = name ? paths[name] : undefined;
  if (!d) return null;
  return {
    body: `<path d="${d}" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>`,
    width: 16,
    height: 16,
  };
}
</script>

<template>
  <div class="starters">
    <p class="heading">{{ heading || fallback }}</p>
    <div class="cards">
      <button
        v-for="(c, i) in props.cards"
        :key="`${c.title}-${i}`"
        class="card"
        type="button"
        @click="emit('pick', c)"
      >
        <Icon v-if="iconFor(c.icon)" class="card-icon" :icon="iconFor(c.icon)!" width="16" />
        <span class="card-title">{{ c.title }}</span>
        <span v-if="c.label" class="card-label">{{ c.label }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped lang="scss">
.starters {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 18px;
  padding: 56px 16px 8px;
}

/* The one line of type in this UI allowed to be large. It is the only screen with
   no content of its own to compete with, and a 15px sentence floating in an empty
   column reads as a placeholder rather than a welcome. */
.heading {
  margin: 0;
  font-size: 21px;
  font-weight: 600;
  letter-spacing: -0.2px;
  color: var(--el-text-color-primary);
  text-align: center;
}

/* Auto-fit rather than a fixed column count: the conversation column changes
   width whenever the dock or the sidebar does, and a card row that reflows is
   better than one that overflows. */
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(168px, 1fr));
  gap: 8px;
  width: 100%;
  max-width: 680px;
}

.card {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
  min-height: 82px;
  padding: 12px 14px;
  text-align: left;
  border: 1px solid var(--el-border-color-light);
  border-radius: 12px;
  background: var(--el-bg-color);
  cursor: pointer;
  transition:
    border-color 0.15s ease,
    background 0.15s ease,
    box-shadow 0.15s ease,
    transform 0.15s ease;

  /* Lifts rather than only tinting: these are the only cards in the app that are
     entirely a click target, and the movement is what says so. */
  &:hover {
    border-color: var(--pg-accent-line);
    box-shadow: var(--el-box-shadow-light);
    transform: translateY(-1px);
  }

  &:active {
    transform: translateY(0);
  }
}

.card-icon {
  color: var(--el-color-primary);
}

.card-title {
  font-size: 13px;
  font-weight: 550;
  line-height: 1.45;
  color: var(--el-text-color-primary);
}

.card-label {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
</style>
