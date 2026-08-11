<script setup lang="ts">
// The pi-go mark: a white π on a black tile, matching the UI's black & white
// scheme. The π is drawn as strokes rather than a font glyph so it renders
// identically on every system.
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
    <rect x="1" y="1" width="30" height="30" rx="8" fill="#1a1a1a" />
    <path
      d="M8.8 11h14.4M13.1 11c0 3.8-.5 6.8-1.6 9.8M18.9 11c0 3.8.5 6.8 1.6 9.8"
      stroke="#fff"
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
</style>
