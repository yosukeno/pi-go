<script setup lang="ts">
// The pi-go mark: a π on a solid tile. The π is drawn as strokes rather than a
// font glyph so it renders identically on every system.
//
// The tile takes the skin's solid — the same colour as the Send button — so the
// mark is in the same family as the interface rather than a black square sitting
// in the corner of a Nord sidebar. currentColor via the wrapper would have been
// simpler but wrong: the stroke has to contrast with the tile, not with the text
// around it.
withDefaults(defineProps<{ size?: number }>(), { size: 22 });
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 32 32"
    fill="none"
    role="img"
    aria-label="pi-go"
    class="logo"
  >
    <rect x="1" y="1" width="30" height="30" rx="8" class="tile" />
    <path
      d="M8.8 11h14.4M13.1 11c0 3.8-.5 6.8-1.6 9.8M18.9 11c0 3.8.5 6.8 1.6 9.8"
      class="glyph"
      stroke-width="2.5"
      stroke-linecap="round"
    />
  </svg>
</template>

<style scoped>
.logo {
  display: block;
  flex-shrink: 0;
}

.tile {
  fill: var(--pg-solid);
}

.glyph {
  stroke: var(--pg-on-solid);
}
</style>
