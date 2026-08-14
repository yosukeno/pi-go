import { alpha, mix, readable } from "./color";

/**
 * A skin, as authored.
 *
 * Twelve colours and two surfaces. That is the entire contract: everything the
 * interface actually styles against — six fills, seven borders, five text
 * weights, a nine-step ramp for each of five semantic colours, shadows, rings,
 * the terminal block, the syntax colours — is derived from these by buildTokens
 * below.
 *
 * The reason for the small seed is not brevity, it is consistency. A skin
 * authored token by token drifts: its hover fill ends up a different distance
 * from its rest fill than the next skin's, and the interface reads as slightly
 * broken in some skins and not others. Deriving means every skin has the same
 * internal proportions and differs only where it is supposed to — in its palette.
 */
export interface ThemeSeed {
  /** Stable id; goes in localStorage and in the data-theme attribute. */
  id: string;
  /** Display name. A proper noun, so it is never translated. */
  name: string;
  mode: "light" | "dark";
  /**
   * Where the palette comes from, shown under the name in the picker. Published
   * palettes are credited by name; the two originals say so.
   */
  origin: string;

  /** The chrome surface: sidebar, dock rail, topbar. */
  page: string;
  /** The content surface: conversation, composer card, dialogs. */
  canvas: string;
  /** Primary text, and the colour every fill and border is mixed towards. */
  ink: string;
  /** Secondary text. Also the comment colour and the scrollbar. */
  muted: string;

  /** The one saturated colour: live state, focus, progress, active session. */
  accent: string;
  success: string;
  warning: string;
  danger: string;
  /** A fifth hue, for the type/violet slot in syntax and the context chart. */
  violet: string;

  /**
   * The affirmative solid (Send, Approve). Light skins default to their own ink,
   * which is the strongest contrast on paper; dark skins default to the accent,
   * because an ink button on a dark canvas is invisible.
   */
  solid?: string;

  /**
   * The terminal block: command output and the shell panel.
   *
   * Explicit per skin rather than derived, and always dark. Terminal output is
   * the one surface with its own convention, and every family that ships a light
   * palette also ships a dark one — so a light skin borrows its own family's dark
   * surface here instead of inventing a grey. That is why Solarized Light's output
   * block is Solarized Dark and not a washed-out beige.
   */
  term: { bg: string; fg: string };
}

/** The built token set: CSS custom property name → value. */
export type Tokens = Record<string, string>;

/**
 * buildTokens turns a seed into every variable the app and Element Plus read.
 *
 * The percentages are the design, and they are the same for all skins:
 *
 *   fills     canvas → ink at 2/4/6/9/13/18%   (rest, hover, press ladder)
 *   borders   canvas → ink at 9/14/20/28/34/42/50%
 *   text      ink, ink→canvas 15%, muted, muted→canvas 30%, muted→canvas 50%
 *   ramps     colour → canvas at 30/50/70/80/90%  (Element's light-3 … light-9)
 *
 * Element's own light-N means "N tenths towards white"; towards the canvas is the
 * same thing on a light skin and the only sensible reading on a dark one, where a
 * tint has to sit *on* the canvas rather than glow above it.
 */
export function buildTokens(seed: ThemeSeed): Tokens {
  const { canvas, ink, muted, mode } = seed;
  const dark = mode === "dark";

  const fill = (amount: number) => mix(canvas, ink, amount);
  const line = (amount: number) => mix(canvas, ink, amount);
  const ramp = (color: string) => ({
    base: color,
    l3: mix(color, canvas, 0.3),
    l5: mix(color, canvas, 0.5),
    l7: mix(color, canvas, 0.7),
    l8: mix(color, canvas, 0.8),
    l9: mix(color, canvas, dark ? 0.86 : 0.9),
    d2: mix(color, dark ? "#ffffff" : "#000000", 0.14),
  });

  const primary = ramp(seed.accent);
  const success = ramp(seed.success);
  const warning = ramp(seed.warning);
  const danger = ramp(seed.danger);
  const info = ramp(muted);

  const solid = seed.solid ?? (dark ? seed.accent : mix(ink, canvas, 0.06));
  const onSolid = readable(solid);
  const solidHover = mix(solid, dark ? "#ffffff" : "#ffffff", 0.16);

  // Shadows are tinted with the ink on light skins — a neutral grey shadow on a
  // warm surface reads as dirt — and plain black on dark ones, where the only way
  // to suggest height is to remove light.
  const shade = dark ? "#000000" : ink;
  const s = (a: number) => alpha(shade, dark ? a * 2.2 : a);

  const semantic = (name: string, r: ReturnType<typeof ramp>): Tokens => ({
    [`--el-color-${name}`]: r.base,
    [`--el-color-${name}-light-3`]: r.l3,
    [`--el-color-${name}-light-5`]: r.l5,
    [`--el-color-${name}-light-7`]: r.l7,
    [`--el-color-${name}-light-8`]: r.l8,
    [`--el-color-${name}-light-9`]: r.l9,
    [`--el-color-${name}-dark-2`]: r.d2,
  });

  return {
    ...semantic("primary", primary),
    ...semantic("success", success),
    ...semantic("warning", warning),
    ...semantic("danger", danger),
    // Element treats error as a synonym of danger and reads both.
    ...semantic("error", danger),
    ...semantic("info", info),

    "--el-bg-color": canvas,
    "--el-bg-color-page": seed.page,
    "--el-bg-color-overlay": dark ? fill(0.05) : canvas,

    "--el-text-color-primary": ink,
    "--el-text-color-regular": mix(ink, canvas, 0.15),
    "--el-text-color-secondary": muted,
    "--el-text-color-placeholder": mix(muted, canvas, 0.3),
    "--el-text-color-disabled": mix(muted, canvas, 0.5),

    "--el-border-color-extra-light": line(0.09),
    "--el-border-color-lighter": line(0.14),
    "--el-border-color-light": line(0.2),
    "--el-border-color": line(0.28),
    "--el-border-color-dark": line(0.34),
    "--el-border-color-darker": line(0.42),
    "--el-border-color-hover": line(0.5),

    "--el-fill-color-blank": canvas,
    "--el-fill-color-extra-light": fill(0.02),
    "--el-fill-color-lighter": fill(0.04),
    "--el-fill-color-light": fill(0.06),
    "--el-fill-color": fill(0.09),
    "--el-fill-color-dark": fill(0.13),
    "--el-fill-color-darker": fill(0.18),

    "--el-disabled-bg-color": fill(0.06),
    "--el-disabled-text-color": mix(muted, canvas, 0.5),
    "--el-disabled-border-color": line(0.2),
    "--el-mask-color": alpha(dark ? "#000000" : ink, 0.45),

    "--el-box-shadow-lighter": `0 1px 2px ${s(0.04)}`,
    "--el-box-shadow-light": `0 1px 2px ${s(0.04)}, 0 6px 18px ${s(0.06)}`,
    "--el-box-shadow": `0 2px 6px ${s(0.06)}, 0 14px 38px ${s(0.1)}`,
    "--el-box-shadow-dark": `0 4px 12px ${s(0.1)}, 0 24px 56px ${s(0.14)}`,

    // ── App tokens ────────────────────────────────────────────────────────
    "--pg-ring": `0 0 0 3px ${alpha(seed.accent, dark ? 0.3 : 0.18)}`,
    "--pg-accent-wash": primary.l9,
    "--pg-accent-line": alpha(seed.accent, dark ? 0.45 : 0.3),

    "--pg-solid": solid,
    "--pg-solid-hover": solidHover,
    "--pg-on-solid": onSolid,

    // The tooltip is a floating chip over an unknown surface, so it inverts on
    // light skins and lifts on dark ones rather than sharing either.
    "--pg-tooltip-bg": dark ? fill(0.16) : mix(ink, "#000000", 0.06),
    "--pg-tooltip-fg": dark ? ink : mix(canvas, "#ffffff", 0.5),

    "--pg-term-bg": seed.term.bg,
    "--pg-term-fg": seed.term.fg,
    "--pg-term-line": mix(seed.term.bg, seed.term.fg, 0.14),
    "--pg-term-fill": mix(seed.term.bg, seed.term.fg, 0.1),

    // Syntax: five hues plus the comment, mapped onto the semantic colours the
    // skin already declared. Nudged towards the ink on light skins, where a
    // published palette's accents are often tuned for its own dark variant and
    // land a shade too light on paper.
    "--pg-syn-comment": muted,
    "--pg-syn-string": dark ? seed.success : mix(seed.success, ink, 0.12),
    "--pg-syn-number": dark ? seed.accent : mix(seed.accent, ink, 0.12),
    "--pg-syn-keyword": dark ? seed.danger : mix(seed.danger, ink, 0.12),
    "--pg-syn-type": dark ? seed.violet : mix(seed.violet, ink, 0.12),
    "--pg-syn-func": dark ? seed.warning : mix(seed.warning, ink, 0.12),

    // The context meter's four categories, in the order it lists them.
    "--pg-chart-overhead": seed.violet,
    "--pg-chart-user": seed.accent,
    "--pg-chart-assistant": seed.success,
    "--pg-chart-tools": seed.warning,

    "--pg-scroll-thumb": alpha(muted, dark ? 0.38 : 0.28),
    "--pg-scroll-thumb-strong": alpha(muted, dark ? 0.55 : 0.4),
    "--pg-scroll-thumb-hover": alpha(muted, dark ? 0.75 : 0.62),
    "--pg-selection-bg": primary.l8,

    // Mermaid runs in JS and cannot read scoped styles, so it reads these.
    "--pg-diagram-node": fill(0.06),
    "--pg-diagram-alt": mix(canvas, seed.warning, dark ? 0.14 : 0.1),
    "--pg-diagram-cluster": mix(canvas, seed.accent, dark ? 0.1 : 0.06),
  };
}
