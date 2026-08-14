/**
 * The colour arithmetic the theme builder runs on.
 *
 * It exists so a theme can be declared as a dozen colours instead of sixty. Every
 * skin needs the same ladders — six fills, seven borders, five text weights, a
 * nine-step ramp per semantic colour — and hand-authoring those for fourteen
 * skins would be eight hundred hex literals nobody could keep consistent. Deriving
 * them means a skin is its palette and nothing else, and that a new one cannot
 * ship with a token missing.
 *
 * Kept to hex and sRGB on purpose. OKLCH would mix more evenly, but every value
 * here comes from a published palette that was hand-tuned in sRGB, and a
 * perceptual space would pull the derived steps off the family's own ramp — the
 * point is to look like Nord, not to look like a correct interpolation of Nord.
 * The one place that matters (are two colours far enough apart to read) is
 * checked with relative luminance below, which is the sRGB definition anyway.
 */

/** rgb is a colour as three 0-255 channels. */
export interface RGB {
  r: number;
  g: number;
  b: number;
}

/** parse accepts #rgb and #rrggbb. Anything else throws — a typo in a palette
 * should fail at boot, not silently paint one token black. */
export function parse(hex: string): RGB {
  const s = hex.trim().replace(/^#/, "");
  const full = s.length === 3 ? s[0] + s[0] + s[1] + s[1] + s[2] + s[2] : s;
  if (!/^[0-9a-fA-F]{6}$/.test(full)) throw new Error(`bad hex colour: ${hex}`);
  return {
    r: parseInt(full.slice(0, 2), 16),
    g: parseInt(full.slice(2, 4), 16),
    b: parseInt(full.slice(4, 6), 16),
  };
}

const clamp = (n: number) => Math.max(0, Math.min(255, Math.round(n)));

export function toHex({ r, g, b }: RGB): string {
  const part = (n: number) => clamp(n).toString(16).padStart(2, "0");
  return `#${part(r)}${part(g)}${part(b)}`;
}

/**
 * mix moves `amount` (0..1) of the way from `from` to `to`.
 *
 * This is the whole engine: a fill is the canvas a few percent towards the ink, a
 * border is the same a bit further, a tint is a semantic colour most of the way
 * back to the canvas. Mixing towards the *canvas* rather than towards white is
 * what makes one formula serve both light and dark skins — on a dark canvas the
 * same call produces a lifted surface instead of a washed-out one.
 */
export function mix(from: string, to: string, amount: number): string {
  const a = parse(from);
  const b = parse(to);
  const t = Math.max(0, Math.min(1, amount));
  return toHex({
    r: a.r + (b.r - a.r) * t,
    g: a.g + (b.g - a.g) * t,
    b: a.b + (b.b - a.b) * t,
  });
}

/** alpha renders a colour as rgba(), for the shadows, rings and washes that have
 * to sit over an unknown surface. */
export function alpha(hex: string, a: number): string {
  const { r, g, b } = parse(hex);
  return `rgba(${r}, ${g}, ${b}, ${Math.max(0, Math.min(1, a))})`;
}

/** luminance is the WCAG relative luminance, used to decide what text can go on
 * top of a solid button whose colour the skin chose. */
export function luminance(hex: string): number {
  const { r, g, b } = parse(hex);
  const channel = (v: number) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

/**
 * readable picks the label colour for a filled surface.
 *
 * Near-black and near-white rather than the skin's own ink: this is the one place
 * where staying in the family loses to being legible, since a solid accent button
 * can land anywhere on the lightness scale and the skin's ink was chosen against
 * the canvas, not against the accent.
 */
export function readable(background: string): string {
  return luminance(background) > 0.55 ? "#141414" : "#ffffff";
}
