<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { markOf } from "./todoMarks";
import type { TodoDetails } from "@/api/types";

const props = defineProps<{
  details: TodoDetails;
  /**
   * superseded means a later write replaced this list. The card stays on the
   * timeline — how the plan changed is worth seeing — but collapses to one line,
   * because several open checklists disagreeing with each other is worse than
   * none.
   */
  superseded?: boolean;
  /**
   * pinned means the bar above the composer is already showing the current plan.
   *
   * The current list then collapses for the same reason a superseded one does —
   * one plan should be open at a time — but it is not dimmed: it is still the
   * plan, it is just not the copy you are meant to read. Without this the newest
   * card and the bar render the same checklist twice, adjacent, whenever the
   * transcript is scrolled to the bottom.
   */
  pinned?: boolean;
}>();

const { t } = useI18n();

// Collapsed by default when there is another copy of this list to read, and always
// openable: the reason to keep an old list on the timeline at all is the occasional
// "when did that item appear".
const open = ref(false);

const done = computed(() => props.details.todos.filter((t) => t.status === "completed").length);
const total = computed(() => props.details.todos.length);
const current = computed(() => props.details.todos.find((t) => t.status === "in_progress"));
const foldable = computed(() => props.superseded === true || props.pinned === true);
const shown = computed(() => !foldable.value || open.value);

// The symbol table lives in todoMarks so the pinned bar above the composer draws
// the same list the same way; see there.
</script>

<template>
  <div class="todo" :class="{ old: superseded }">
    <!-- The header is the whole card when collapsed, so it has to carry the two
         facts worth having: how far along, and what was being worked on. -->
    <div class="head" :class="{ clickable: foldable }" @click="foldable && (open = !open)">
      <span v-if="total === 0" class="meta">{{ t("todoResult.cleared") }}</span>
      <template v-else>
        <span class="count">{{ done }}/{{ total }}</span>
        <span v-if="current" class="cur">{{ current.task }}</span>
        <span v-else class="meta">{{ t("todoResult.noCurrent") }}</span>
      </template>
      <span v-if="superseded" class="badge">{{ t("todoResult.superseded") }}</span>
    </div>

    <ol v-if="shown && total" class="list">
      <li v-for="(t, i) in details.todos" :key="i" :class="t.status">
        <span class="mark">{{ markOf(t.status) }}</span>
        <span class="task">{{ t.task }}</span>
      </li>
    </ol>
  </div>
</template>

<style scoped lang="scss">
.head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}

.head.clickable {
  cursor: pointer;
}

.count {
  font-family: var(--pg-mono);
  font-variant-numeric: tabular-nums;
}

/* The one line that answers "what now", so it is the only part of the header at
   full strength. */
.cur {
  color: var(--el-text-color-primary);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.badge {
  margin-left: auto;
  flex: 0 0 auto;
  font-size: 10px;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--el-fill-color-light);
}

.list {
  list-style: none;
  margin: 0;
  padding: 8px 12px;
  background: var(--el-fill-color-lighter);
  border: 1px solid var(--el-border-color-extra-light);
  border-radius: 10px;
  font-size: 12px;
  line-height: 1.75;
}

.list li {
  display: flex;
  gap: 8px;
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

/* Done work recedes; the live item is the only one at full contrast. Blocked is
   the one state that has to interrupt, because it is the one the completion rule
   redirects a failure into — an item nobody looks at is how a failed
   verification becomes a silent success. */
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

/* A replaced list is history: dimmed as a whole so it cannot be mistaken for the
   current plan even when expanded. */
.todo.old {
  opacity: 0.55;
}
</style>
