import type { ThemeSeed } from "./build";

/**
 * The skins.
 *
 * Two of them are this project's own; the other twelve are established
 * developer-tool palettes, used because they are already the answer to "what does
 * a comfortable working surface look like" — each one has had years of eyes on it,
 * and someone who works in Nord all day wants their agent in Nord too. Every
 * borrowed palette is credited by name in `origin`, which the picker shows, and
 * the hex values are the published ones.
 *
 * Where a value is adjusted it is because of what this interface is and theirs is
 * not: an editor theme's "comment" colour is tuned to recede behind code, and the
 * same colour used for a UI label is too quiet to read. Those cases say so.
 *
 * Sources: Catppuccin (catppuccin.com/palette), Rosé Pine
 * (rosepinetheme.com/palette), Everforest (sainnhe/everforest palette.md), Nord
 * (nordtheme.com), Tokyo Night (folke/tokyonight.nvim), Gruvbox
 * (morhetz/gruvbox), Dracula (draculatheme.com), Solarized (Ethan Schoonover),
 * One Dark (Atom), Primer (GitHub).
 */
export const THEME_SEEDS: ThemeSeed[] = [
  // ── Light ───────────────────────────────────────────────────────────────
  {
    id: "paper",
    name: "Paper",
    mode: "light",
    origin: "pi-go",
    // The house skin: warm paper with a clay accent. Its reasoning is in
    // styles.scss — chrome on paper, content on white, one saturated colour spent
    // only on live state.
    page: "#f6f5f2",
    canvas: "#ffffff",
    ink: "#22201d",
    muted: "#79736a",
    accent: "#bd5f3c",
    success: "#2f8659",
    warning: "#b07d1c",
    danger: "#c0392b",
    violet: "#7a5ea8",
    term: { bg: "#23201d", fg: "#d9d5cf" },
  },
  {
    id: "primer-light",
    name: "Primer Light",
    mode: "light",
    origin: "GitHub Primer",
    // The neutral option, and the one to reach for when a screenshot has to look
    // like nothing in particular.
    page: "#f6f8fa",
    canvas: "#ffffff",
    ink: "#1f2328",
    muted: "#59636e",
    accent: "#0969da",
    success: "#1a7f37",
    warning: "#9a6700",
    danger: "#d1242f",
    violet: "#8250df",
    term: { bg: "#0d1117", fg: "#e6edf3" },
  },
  {
    id: "latte",
    name: "Catppuccin Latte",
    mode: "light",
    origin: "Catppuccin",
    // base as the canvas, mantle as the chrome — the same order the family's own
    // editor ports use. Text is `text`, muted is `subtext0`.
    page: "#e6e9ef",
    canvas: "#eff1f5",
    ink: "#4c4f69",
    muted: "#6c6f85",
    accent: "#1e66f5",
    success: "#40a02b",
    warning: "#df8e1d",
    danger: "#d20f39",
    violet: "#8839ef",
    term: { bg: "#1e1e2e", fg: "#cdd6f4" },
  },
  {
    id: "rose-pine-dawn",
    name: "Rosé Pine Dawn",
    mode: "light",
    origin: "Rosé Pine",
    // Iris as the accent rather than Love: Love is the family's red and this
    // interface needs a red that means "destructive", so the two cannot be the
    // same colour. Foam stands in for green — the palette has no green, and a
    // borrowed one would not belong.
    page: "#faf4ed",
    canvas: "#fffaf3",
    ink: "#464261",
    muted: "#797593",
    accent: "#907aa9",
    success: "#56949f",
    warning: "#ea9d34",
    danger: "#b4637a",
    violet: "#d7827e",
    term: { bg: "#191724", fg: "#e0def4" },
  },
  {
    id: "everforest-light",
    name: "Everforest Light",
    mode: "light",
    origin: "Everforest",
    // Medium contrast: bg0 as the canvas, bg_dim as the chrome. Green is the
    // family's signature, so it takes the accent slot and aqua carries success.
    page: "#efebd4",
    canvas: "#fdf6e3",
    ink: "#5c6a72",
    muted: "#939f91",
    accent: "#8da101",
    success: "#35a77c",
    warning: "#dfa000",
    danger: "#f85552",
    violet: "#df69ba",
    term: { bg: "#2d353b", fg: "#d3c6aa" },
  },
  {
    id: "solarized-light",
    name: "Solarized Light",
    mode: "light",
    origin: "Ethan Schoonover",
    // base3 canvas, base2 chrome, base01 text, base1 muted — Solarized's own
    // assignment, which is the whole point of it.
    page: "#eee8d5",
    canvas: "#fdf6e3",
    ink: "#586e75",
    muted: "#93a1a1",
    accent: "#268bd2",
    success: "#859900",
    warning: "#b58900",
    danger: "#dc322f",
    violet: "#6c71c4",
    term: { bg: "#002b36", fg: "#93a1a1" },
  },

  // ── Dark ────────────────────────────────────────────────────────────────
  {
    id: "graphite",
    name: "Graphite",
    mode: "dark",
    origin: "pi-go",
    // Paper's night side, and the default for anyone whose system asks for dark:
    // the same warm neutrals inverted, same clay accent, nothing to get used to.
    page: "#1a1917",
    canvas: "#211f1d",
    ink: "#eae6df",
    muted: "#a09a90",
    accent: "#e08a63",
    success: "#5fbb8b",
    warning: "#e0b356",
    danger: "#e57668",
    violet: "#b39ae0",
    term: { bg: "#171614", fg: "#e0dbd3" },
  },
  {
    id: "mocha",
    name: "Catppuccin Mocha",
    mode: "dark",
    origin: "Catppuccin",
    // Mauve as the accent, which is the flavour's face. crust for the terminal,
    // one step below the chrome.
    page: "#181825",
    canvas: "#1e1e2e",
    ink: "#cdd6f4",
    muted: "#a6adc8",
    accent: "#cba6f7",
    success: "#a6e3a1",
    warning: "#f9e2af",
    danger: "#f38ba8",
    violet: "#b4befe",
    term: { bg: "#11111b", fg: "#cdd6f4" },
  },
  {
    id: "tokyo-night",
    name: "Tokyo Night",
    mode: "dark",
    origin: "enkia / folke",
    // muted is lifted off the palette's `comment` (#565f89): a comment colour is
    // built to recede behind code, and at UI label size on this canvas it stops
    // being readable rather than merely quiet.
    page: "#16161e",
    canvas: "#1a1b26",
    ink: "#c0caf5",
    muted: "#7e88b8",
    accent: "#7aa2f7",
    success: "#9ece6a",
    warning: "#e0af68",
    danger: "#f7768e",
    violet: "#bb9af7",
    term: { bg: "#15161e", fg: "#c0caf5" },
  },
  {
    id: "nord",
    name: "Nord",
    mode: "dark",
    origin: "Arctic Ice Studio",
    // nord0 is the canvas; the chrome is one step below it, because Nord's own
    // convention of a *lighter* sidebar inverts the surface logic the rest of
    // this interface is built on. Frost nord8 accents, Aurora carries the
    // semantics. muted sits between nord4 and nord3, neither of which works as a
    // secondary label here.
    page: "#282e39",
    canvas: "#2e3440",
    ink: "#eceff4",
    muted: "#a8b1c2",
    accent: "#88c0d0",
    success: "#a3be8c",
    warning: "#ebcb8b",
    danger: "#bf616a",
    violet: "#b48ead",
    term: { bg: "#242933", fg: "#d8dee9" },
  },
  {
    id: "gruvbox-dark",
    name: "Gruvbox Dark",
    mode: "dark",
    origin: "morhetz",
    // dark0 canvas, dark0_hard chrome, the bright ramp for accents — the soft
    // ramp is tuned for light gruvbox and goes muddy here.
    page: "#1d2021",
    canvas: "#282828",
    ink: "#ebdbb2",
    muted: "#a89984",
    accent: "#fabd2f",
    success: "#b8bb26",
    warning: "#fe8019",
    danger: "#fb4934",
    violet: "#d3869b",
    term: { bg: "#1d2021", fg: "#ebdbb2" },
  },
  {
    id: "rose-pine",
    name: "Rosé Pine",
    mode: "dark",
    origin: "Rosé Pine",
    // base canvas, surface chrome. Iris accents for the same reason Dawn does:
    // Love has to stay available as the destructive red.
    page: "#1f1d2e",
    canvas: "#191724",
    ink: "#e0def4",
    muted: "#908caa",
    accent: "#c4a7e7",
    success: "#9ccfd8",
    warning: "#f6c177",
    danger: "#eb6f92",
    violet: "#ebbcba",
    term: { bg: "#16141f", fg: "#e0def4" },
  },
  {
    id: "dracula",
    name: "Dracula",
    mode: "dark",
    origin: "Dracula",
    // Purple accents, orange for warning rather than the palette's yellow, which
    // at #f1fa8c is too pale to read as a caution on this background. muted is
    // lifted off `comment` (#6272a4) for the Tokyo Night reason.
    page: "#21222c",
    canvas: "#282a36",
    ink: "#f8f8f2",
    muted: "#8f97c4",
    accent: "#bd93f9",
    success: "#50fa7b",
    warning: "#ffb86c",
    danger: "#ff5555",
    violet: "#ff79c6",
    term: { bg: "#21222c", fg: "#f8f8f2" },
  },
  {
    id: "one-dark",
    name: "One Dark",
    mode: "dark",
    origin: "Atom",
    // ink is brighter than Atom's #abb2bf: that value is a code foreground on a
    // small type size, and as this interface's primary text it sits under the
    // contrast a paragraph needs.
    page: "#21252b",
    canvas: "#282c34",
    ink: "#d7dae0",
    muted: "#9aa1ad",
    accent: "#61afef",
    success: "#98c379",
    warning: "#e5c07b",
    danger: "#e06c75",
    violet: "#c678dd",
    term: { bg: "#21252b", fg: "#abb2bf" },
  },
  {
    id: "primer-dark",
    name: "Primer Dark",
    mode: "dark",
    origin: "GitHub Primer",
    // canvas.default as the content surface and canvas.subtle as the chrome, which
    // is GitHub's own arrangement; the terminal takes canvas.inset.
    page: "#161b22",
    canvas: "#0d1117",
    ink: "#e6edf3",
    muted: "#8b949e",
    accent: "#4493f8",
    success: "#3fb950",
    warning: "#d29922",
    danger: "#f85149",
    violet: "#ab7df8",
    term: { bg: "#010409", fg: "#e6edf3" },
  },
];

/** The skin a first visit gets, per what the system asks for. */
export const DEFAULT_LIGHT = "paper";
export const DEFAULT_DARK = "graphite";
