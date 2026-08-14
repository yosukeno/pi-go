<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import Logo from "./Logo.vue";
import { setToken } from "@/api/client";

// The no-token page, sibling of the cannot-connect screen (Unreachable.vue):
// the app's chrome stays mounted behind a veil while this takes the viewport.
// Entering a token persists it everywhere readToken looks (sessionStorage plus
// the URL) and reloads — boot then runs with the Authorization header in place.
defineProps<{ rejected?: boolean }>();
const { t } = useI18n();

const value = ref("");
const shake = ref(false);

function submit() {
  const trimmed = value.value.trim();
  if (!trimmed) {
    shake.value = true;
    window.setTimeout(() => (shake.value = false), 400);
    return;
  }
  setToken(trimmed);
  window.location.reload();
}
</script>

<template>
  <div class="veil">
    <div class="card" :class="{ shake }">
      <div class="mark">
        <Logo :size="40" />
      </div>
      <h1>{{ t("tokenGate.title") }}</h1>
      <p class="sub">{{ t("tokenGate.subtitle") }}</p>

      <p v-if="rejected" class="rejected">{{ t("tokenGate.rejected") }}</p>

      <ul class="causes">
        <i18n-t keypath="tokenGate.hintStartup" tag="li" scope="global">
          <template #url><code>?token=</code></template>
        </i18n-t>
        <i18n-t keypath="tokenGate.hintDocker" tag="li" scope="global">
          <template #url2><code>http://localhost:17779/?token=…</code></template>
        </i18n-t>
      </ul>

      <form class="entry" @submit.prevent="submit">
        <input
          v-model="value"
          type="text"
          :placeholder="t('tokenGate.placeholder')"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
        />
        <button class="primary" type="submit">{{ t("tokenGate.enter") }}</button>
      </form>
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

  &.shake {
    animation: shake 0.4s ease-out;
  }
}

@keyframes rise {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
}

/* Empty submit: a nudge, not a punishment. */
@keyframes shake {
  20%,
  60% {
    transform: translateX(-6px);
  }
  40%,
  80% {
    transform: translateX(6px);
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

/* The token was presented and refused — different advice from "bring one". */
.rejected {
  margin: 0 0 14px;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--el-color-danger);
}

/* Where the token lives, in the same checklist voice as Unreachable's causes. */
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

    code {
      padding: 1px 5px;
      border-radius: 4px;
      background: var(--el-fill-color);
      border: 1px solid var(--el-border-color-lighter);
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-size: 11px;
    }
  }
}

.entry {
  display: flex;
  gap: 10px;

  input {
    flex: 1;
    min-width: 0;
    padding: 8px 12px;
    border: 1px solid var(--el-border-color);
    border-radius: 8px;
    background: var(--el-bg-color);
    color: var(--el-text-color-primary);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 13px;

    &:focus {
      outline: none;
      border-color: var(--el-color-primary);
    }
  }

  .primary {
    padding: 8px 20px;
    border: 1px solid var(--el-color-primary);
    border-radius: 8px;
    background: var(--el-color-primary);
    color: #fff;
    font-size: 13px;
    cursor: pointer;

    &:hover {
      opacity: 0.88;
    }
  }
}
</style>
