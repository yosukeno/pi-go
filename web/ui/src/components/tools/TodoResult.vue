<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { TodoDetails, TodoStatus } from "@/api/types";

const props = defineProps<{
  details: TodoDetails;
  /**
   * superseded means a later write replaced this list. The card stays on the
   * timeline — how the plan changed is worth seeing — but collapses to one line,
   * because several open checklists disagreeing with each other is worse than
   * none.
   */
  superseded?: boolean;
}>();

const { t } = useI18n();

// Collapsed by default when superseded, and openable: the reason to keep an old
// list at all is the occasional "when did that item appear".
const open = ref(false);

const done = computed(() => props.details.todos.filter((t) => t.status === "completed").length);
const total = computed(() => props.details.todos.length);
const current = computed(() => props.details.todos.find((t) => t.status === "in_progress"));
const shown = computed(() => !props.superseded || open.value);

// Marks rather than the status words. Five spelled-out statuses down the left
// margin push every task into a ragged column and make the list harder to scan
// than the plain numbered text the model reads — while the state of an item is
// exactly the thing that is glanceable and the text is not.
const MARKS: Record<TodoStatus, string> = {
  pending: "○",
  in_progress: "▸",
  completed: "✓",
  cancelled: "–",
  blocked: "✗",
};
</script>

<template>
  <div class="todo" :class="{ old: superseded }">
    <!-- The header is the whole card when collapsed, so it has to carry the two
         facts worth having: how far along, and what was being worked on. -->
    <div class="head" :class="{ clickable: superseded }" @click="superseded && (open = !open)">
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
        <span class="mark">{{ MARKS[t.status] ?? "○" }}</span>
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
  font-family: ui-monospace, monospace;
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
  padding: 1px 6px;
  border-radius: 3px;
  background: var(--el-fill-color);
}

.list {
  list-style: none;
  margin: 0;
  padding: 6px 10px;
  background: var(--el-fill-color-lighter);
  border-radius: 4px;
  font-size: 12px;
  line-height: 1.7;
}

.list li {
  display: flex;
  gap: 8px;
  color: var(--el-text-color-regular);
}

.mark {
  flex: 0 0 auto;
  width: 1em;
  font-family: ui-monospace, monospace;
  color: var(--el-text-color-secondary);
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
