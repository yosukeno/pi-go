<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { markOf } from "./tools/todoMarks";
import type { TodoItem } from "@/api/types";

/**
 * The plan, pinned above the composer.
 *
 * The task list used to exist only as a tool-call card inside the scrolling
 * transcript, which meant it scrolled away: a plan written on turn 7 was off
 * screen by turn 11, exactly when "which step are we on" is the question. Pinned,
 * it answers that without scrolling back, and the steps visibly tick over as the
 * run works through them.
 *
 * It lives in the composer column rather than in the scroller, so it is fixed by
 * position and needs no sticky rules — and it sits next to the other things
 * decided here (the context warning, the model picker) rather than in the topbar,
 * which is for facts about the session and not about this run.
 *
 * Collapsed is the default and it is not "hidden": one line carrying how far
 * along, what is being worked on, and whether anything is stuck. That is the
 * state it spends almost all its life in; expanding is for when the order of what
 * is left matters.
 *
 * The choice is not persisted, which is a reversal. It used to be remembered
 * across sessions on the grounds that it is a preference about screen space —
 * true, but the consequence was that opening it once left every later run
 * starting expanded, a checklist eating a third of the composer column before
 * anyone had asked it anything. Collapsed is right for the first look at a plan;
 * a fresh page gets it, and within the session the choice sticks.
 */
const props = defineProps<{
  todos: TodoItem[];
  /**
   * busy says a run is in flight. It only changes how the live step is drawn —
   * a spinner instead of a caret, and a sheen crossing the fill — and that is
   * worth the prop: between two of the model's own list writes nothing here
   * moves, and a still progress bar during a four-minute tool call is
   * indistinguishable from a stuck one.
   */
  busy?: boolean;
}>();

const { t } = useI18n();

// The old persisted preference. Removed rather than left behind: it is what makes
// a returning user's bar open by itself, and reading it here is the only thing
// that gave it meaning.
localStorage.removeItem("pi-go.todoBar.open");

const open = ref(false);

const total = computed(() => props.todos.length);
// Cancelled counts as settled, not as outstanding: a plan of five with two
// dropped is 3/3 rather than 3/5 forever, and the alternative reads as a run that
// stalled. The list below still shows them struck through, so nothing is hidden.
//
// Blocked settles too. It is an outcome — the completion rule sends failures here
// — and a bar that treats it as outstanding says a finished run is unfinished,
// which is the same lie in the other direction. The badge is what makes sure it
// is not read as success.
const settled = computed(
  () => props.todos.filter((x) => x.status !== "pending" && x.status !== "in_progress").length,
);
const done = computed(() => props.todos.filter((x) => x.status === "completed").length);
const current = computed(() => props.todos.find((x) => x.status === "in_progress"));
const blocked = computed(() => props.todos.filter((x) => x.status === "blocked").length);
const allSettled = computed(() => total.value > 0 && settled.value === total.value);

/**
 * The fill, and the one place this component does not simply count.
 *
 * Counting settled items is honest and reads as broken: the model writes its list
 * when a step finishes, so a bar keyed to completions sits still for the entire
 * length of a step and then jumps a whole segment. On a four-item plan that is a
 * bar which is wrong — visibly behind what the header line says — most of the
 * time it is on screen.
 *
 * So a step in progress is worth part of a step. Not a fabricated ramp that
 * creeps forward on a timer, which would be inventing progress nobody reported:
 * one fixed fraction, moving exactly when the model says a step has started.
 * Starting step 3 of 4 shows half of the third segment filled, which is both what
 * the header says and the reading a person would give it unprompted.
 */
const IN_PROGRESS_CREDIT = 0.5;
const pct = computed(() => {
  if (!total.value) return 0;
  const credit = settled.value + (current.value ? IN_PROGRESS_CREDIT : 0);
  return Math.min(100, Math.round((credit / total.value) * 100));
});

// The count is completions over total, unchanged: "3/4" is the plainest possible
// statement and the one the transcript's inline cards make too. Settled-but-not-
// completed items are the badge's job, not this number's.
const label = computed(() => `${done.value}/${total.value}`);

// Which of the four states the head is in. One computed rather than four
// conditions in the template, so the glyph, the tint and the text cannot end up
// describing different things.
const phase = computed<"done" | "blocked" | "running" | "idle">(() => {
  if (allSettled.value) return blocked.value ? "blocked" : "done";
  if (current.value) return props.busy ? "running" : "idle";
  return "idle";
});
</script>

<template>
  <div class="todo-bar" :class="[{ open }, `is-${phase}`]">
    <button
      class="head"
      :aria-expanded="open"
      :aria-label="open ? t('todoBar.collapse') : t('todoBar.expand')"
      @click="open = !open"
    >
      <!-- The glyph is the status, so it replaces the caret rather than sitting
           next to it: two symbols competing on the same line, one of which only
           ever says "there is more below", is one too many. Fold state rides on
           the chevron at the far right instead. -->
      <span class="glyph" aria-hidden="true">
        <span v-if="phase === 'done'" class="tick">✓</span>
        <span v-else-if="phase === 'blocked'" class="warn">!</span>
        <span v-else-if="phase === 'running'" class="spin" />
        <span v-else class="dot" />
      </span>

      <!-- The live item at full strength, because it is the whole reason the bar
           is here. Finished, it says so instead: a bar that keeps naming the last
           step it worked on is the version of this that made a completed run look
           like it was still going. With nothing in progress and work left, the
           gap between two steps is a real state and gets said out loud, because a
           blank line reads as a bug. -->
      <span v-if="phase === 'done'" class="cur muted">{{ t("todoBar.allDone", { n: total }) }}</span>
      <span v-else-if="current" class="cur">{{ current.task }}</span>
      <span v-else class="cur meta">{{ t("todoBar.noCurrent") }}</span>

      <!-- Blocked interrupts, and it survives completion: it is where the
           completion rule sends a failure, so an unread blocked item is how a
           failed check becomes a silent success. -->
      <span v-if="blocked" class="badge bad">{{ t("todoBar.blocked", { n: blocked }) }}</span>

      <span class="count">{{ label }}</span>
      <span class="track" role="progressbar" :aria-valuenow="pct" aria-valuemin="0" aria-valuemax="100">
        <span class="fill" :class="{ live: phase === 'running' }" :style="{ width: pct + '%' }" />
      </span>
      <span class="caret" aria-hidden="true">{{ open ? "▾" : "▸" }}</span>
    </button>

    <!-- Capped and scrollable: the composer column steals its height from the
         conversation, so a twenty-item plan must not push the transcript off
         screen just because someone opened it once. -->
    <ol v-if="open" class="list">
      <li v-for="(x, i) in todos" :key="i" :class="[x.status, { live: busy && x.status === 'in_progress' }]">
        <span class="mark">{{ markOf(x.status) }}</span>
        <span class="task">{{ x.task }}</span>
      </li>
    </ol>
  </div>
</template>

<style scoped lang="scss">
.todo-bar {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: var(--el-bg-color);
  box-shadow: var(--el-box-shadow-lighter);
  margin-bottom: 8px;
  overflow: hidden;
  transition: border-color var(--pg-transition);

  &.is-running {
    border-color: var(--pg-accent-line);
  }

  &.is-done {
    border-color: color-mix(in srgb, var(--el-color-success) 26%, transparent);
  }

  &.is-blocked {
    border-color: color-mix(in srgb, var(--el-color-warning) 34%, transparent);
  }
}

.head {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px 12px;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
  transition: background var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-extra-light);
  }
}

/* One 14px slot whatever it holds, so the text after it does not shift when the
   phase changes. */
.glyph {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  font-size: 12px;
  line-height: 1;
}

.tick {
  color: var(--el-color-success);
  font-weight: 700;
}

.warn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--el-color-warning);
  color: #fff;
  font-size: 10px;
  font-weight: 700;
}

.dot {
  width: 7px;
  height: 7px;
  border: 1.5px solid var(--el-text-color-placeholder);
  border-radius: 50%;
}

/* A ring with one open quadrant: the only shape that reads as "working" without a
   label. Accent-coloured, like everything else in this UI that is alive. */
.spin {
  width: 12px;
  height: 12px;
  border: 2px solid var(--el-color-primary-light-7);
  border-top-color: var(--el-color-primary);
  border-radius: 50%;
  animation: todo-spin 0.75s linear infinite;
}

@keyframes todo-spin {
  to {
    transform: rotate(360deg);
  }
}

.cur {
  flex: 1 1 auto;
  min-width: 0;
  color: var(--el-text-color-primary);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cur.meta,
.cur.muted {
  color: var(--el-text-color-secondary);
  font-weight: 500;
}

.count {
  flex: 0 0 auto;
  font-family: var(--pg-mono);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-secondary);
}

/* Narrow on purpose: a progress bar wide enough to be a feature would crowd out
   the item text, which is the part that says what is happening. */
.track {
  flex: 0 0 72px;
  height: 4px;
  border-radius: 999px;
  background: var(--el-fill-color);
  overflow: hidden;
}

.fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--el-color-primary);
  /* Slow enough to be seen as motion rather than a jump-cut; the whole point of
     the partial credit above is that this animates on both events (a step
     starting and a step finishing) instead of once. */
  transition: width 0.45s cubic-bezier(0.22, 0.61, 0.36, 1);
}

/* A sheen crossing the filled part while a run is in flight. It carries no
   information — the width does that — and it is the difference between "waiting
   on a tool" and "hung", which nothing else on this line can say. */
.fill.live {
  background-image: linear-gradient(
    90deg,
    transparent 0%,
    rgba(255, 255, 255, 0.55) 50%,
    transparent 100%
  );
  background-size: 60% 100%;
  background-repeat: no-repeat;
  animation: todo-sheen 1.6s ease-in-out infinite;
}

@keyframes todo-sheen {
  0% {
    background-position: -60% 0;
  }

  100% {
    background-position: 160% 0;
  }
}

.is-done .fill {
  background: var(--el-color-success);
}

.is-blocked .fill {
  background: var(--el-color-warning);
}

.caret {
  flex: 0 0 auto;
  color: var(--el-text-color-placeholder);
  font-size: 10px;
}

.badge {
  flex: 0 0 auto;
  font-size: 10px;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--el-fill-color);

  &.bad {
    background: var(--el-color-warning-light-9);
    color: var(--el-color-warning);
  }
}

.list {
  list-style: none;
  margin: 0;
  padding: 2px 12px 10px;
  max-height: 30vh;
  overflow-y: auto;
  font-size: 12px;
  line-height: 1.75;
  border-top: 1px solid var(--el-border-color-extra-light);
}

.list li {
  display: flex;
  gap: 9px;
  padding: 1px 0;
  color: var(--el-text-color-regular);
}

.mark {
  flex: 0 0 auto;
  width: 1em;
  font-family: var(--pg-mono);
  color: var(--el-text-color-placeholder);
}

.task {
  overflow-wrap: anywhere;
}

/* The same reading as the inline card, deliberately: done work recedes, the live
   item is the only one at full contrast, blocked is the one state that shouts. */
li.completed {
  color: var(--el-text-color-secondary);

  .mark {
    color: var(--el-color-success);
  }

  .task {
    text-decoration: line-through;
    text-decoration-color: var(--el-border-color);
  }
}

li.in_progress {
  color: var(--el-text-color-primary);
  font-weight: 600;

  .mark {
    color: var(--el-color-primary);
  }

  /* Mid-run the live row's marker breathes, matching the head's spinner: with the
     list open the head is the part that scrolled out of sight. */
  &.live .mark {
    animation: todo-pulse 1.4s ease-in-out infinite;
  }
}

@keyframes todo-pulse {
  0%,
  100% {
    opacity: 1;
  }

  50% {
    opacity: 0.35;
  }
}

li.cancelled {
  color: var(--el-text-color-disabled);

  .task {
    text-decoration: line-through;
  }
}

li.blocked {
  color: var(--el-color-warning);

  .mark {
    color: var(--el-color-warning);
  }
}
</style>
