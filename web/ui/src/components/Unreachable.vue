<script setup lang="ts">
import { useI18n } from "vue-i18n";
import Logo from "./Logo.vue";
import type { Outage } from "@/agent/useAgentStream";

// The cannot-connect page, modelled on a browser's site-unreachable screen:
// the app's chrome stays mounted behind a veil and this takes over the
// viewport. The veil is near-opaque rather than a dimmed preview — everything
// behind it is stale by definition, so showing it would only tease.
defineProps<{ outage: Outage | null }>();
const emit = defineEmits<{ retry: [] }>();
const { t } = useI18n();

const reload = () => window.location.reload();
</script>

<template>
  <div class="veil">
    <div class="card">
      <div class="mark">
        <Logo :size="40" />
      </div>
      <h1>{{ t("unreachable.title") }}</h1>
      <p class="sub">{{ t("unreachable.subtitle") }}</p>

      <ul class="causes">
        <i18n-t keypath="unreachable.causeProcess" tag="li" scope="global">
          <template #cmd><code>pi-go -web</code></template>
          <template #keys><kbd>Ctrl</kbd>+<kbd>C</kbd></template>
        </i18n-t>
        <li>{{ t("unreachable.causeNetwork") }}</li>
        <li>{{ t("unreachable.causeToken") }}</li>
      </ul>

      <p v-if="outage?.message" class="detail">{{ outage.message }}</p>

      <p class="attempts" :class="{ stopped: outage?.gaveUp }">
        <span class="dot" />
        <template v-if="outage?.gaveUp">{{ t("unreachable.gaveUp") }}</template>
        <template v-else-if="outage && outage.attempts > 1">{{ t("unreachable.retryingNth", { n: outage.attempts }) }}</template>
        <template v-else>{{ t("unreachable.retrying") }}</template>
      </p>

      <div class="actions">
        <button class="primary" @click="emit('retry')">{{ t("unreachable.reconnectNow") }}</button>
        <button class="ghost" @click="reload">{{ t("unreachable.reload") }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.veil {
  position: fixed;
  inset: 0;
  z-index: 1000; /* below element-plus poppers, above everything of ours */
  display: grid;
  place-items: center;
  background: color-mix(in srgb, var(--el-bg-color) 92%, transparent);
  backdrop-filter: blur(8px);
}

.card {
  max-width: 460px;
  padding: 32px;
  text-align: center;
  animation: rise 0.25s ease-out;
}

@keyframes rise {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
}

.mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 76px;
  height: 76px;
  border-radius: 50%;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  margin-bottom: 20px;
}

h1 {
  margin: 0 0 8px;
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.sub {
  margin: 0 0 20px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--el-text-color-secondary);
}

/* The checklist, like the browser's "check your proxy / firewall" block. */
.causes {
  margin: 0 0 16px;
  padding: 14px 18px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: var(--el-fill-color-lighter);
  list-style: none;
  text-align: left;

  li {
    position: relative;
    padding-left: 16px;
    font-size: 12.5px;
    line-height: 1.7;
    color: var(--el-text-color-regular);

    & + li {
      margin-top: 6px;
    }

    &::before {
      content: "";
      position: absolute;
      left: 2px;
      top: 0.75em;
      width: 5px;
      height: 5px;
      border-radius: 50%;
      background: var(--el-text-color-placeholder);
    }

    code,
    kbd {
      padding: 1px 5px;
      border-radius: 4px;
      background: var(--el-fill-color);
      border: 1px solid var(--el-border-color-lighter);
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-size: 11px;
    }
  }
}

/* The raw error, for the user who wants the receipt. */
.detail {
  margin: 0 0 14px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
  color: var(--el-text-color-placeholder);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.attempts {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  margin: 0 0 22px;
  font-size: 12px;
  color: var(--el-text-color-secondary);

  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--el-color-warning);
    animation: breathe 1.4s ease-in-out infinite;
  }

  /* gave up: a still, red dot — no more motion to promise progress with. */
  &.stopped .dot {
    background: var(--el-color-danger);
    animation: none;
  }
}

@keyframes breathe {
  50% {
    opacity: 0.3;
  }
}

.actions {
  display: flex;
  justify-content: center;
  gap: 10px;

  button {
    padding: 8px 20px;
    border-radius: 8px;
    font-size: 13px;
    cursor: pointer;
  }

  .primary {
    border: 1px solid var(--el-color-primary);
    background: var(--el-color-primary);
    color: #fff;

    &:hover {
      opacity: 0.88;
    }
  }

  .ghost {
    border: 1px solid var(--el-border-color);
    background: transparent;
    color: var(--el-text-color-regular);

    &:hover {
      background: var(--el-fill-color);
    }
  }
}
</style>
