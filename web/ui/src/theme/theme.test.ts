import { describe, expect, it } from "vitest";
import { luminance, mix, parse, readable } from "./color";
import { buildTokens } from "./build";
import { THEME_SEEDS, DEFAULT_DARK, DEFAULT_LIGHT } from "./themes";

describe("colour arithmetic", () => {
  it("parses both hex forms and rejects anything else", () => {
    expect(parse("#fff")).toEqual({ r: 255, g: 255, b: 255 });
    expect(parse("1e1e2e")).toEqual({ r: 30, g: 30, b: 46 });
    // A typo in a palette has to fail at boot rather than paint one token black.
    expect(() => parse("#12345")).toThrow();
    expect(() => parse("nord0")).toThrow();
  });

  it("mixes towards the target and clamps the ends", () => {
    expect(mix("#000000", "#ffffff", 0.5)).toBe("#808080");
    expect(mix("#000000", "#ffffff", 0)).toBe("#000000");
    expect(mix("#000000", "#ffffff", 2)).toBe("#ffffff");
  });

  it("puts light text on a dark solid and dark text on a light one", () => {
    expect(readable("#1e1e2e")).toBe("#ffffff");
    expect(readable("#f9e2af")).toBe("#141414");
  });
});

describe("skins", () => {
  it("ships at least ten, in both modes", () => {
    expect(THEME_SEEDS.length).toBeGreaterThanOrEqual(10);
    expect(THEME_SEEDS.filter((s) => s.mode === "light").length).toBeGreaterThan(2);
    expect(THEME_SEEDS.filter((s) => s.mode === "dark").length).toBeGreaterThan(2);
  });

  it("has unique ids and names, and the two defaults exist", () => {
    const ids = THEME_SEEDS.map((s) => s.id);
    expect(new Set(ids).size).toBe(ids.length);
    const names = THEME_SEEDS.map((s) => s.name);
    expect(new Set(names).size).toBe(names.length);
    expect(ids).toContain(DEFAULT_LIGHT);
    expect(ids).toContain(DEFAULT_DARK);
  });

  // The point of deriving rather than authoring tokens: no skin can ship with one
  // missing. Compared against the house skin's set, which is the one the fallback
  // block in styles.scss mirrors.
  it("builds the same token set for every skin", () => {
    const reference = Object.keys(buildTokens(THEME_SEEDS[0])).sort();
    for (const seed of THEME_SEEDS) {
      expect(Object.keys(buildTokens(seed)).sort(), seed.id).toEqual(reference);
    }
  });

  it("produces a complete, well-formed value for every token", () => {
    for (const seed of THEME_SEEDS) {
      for (const [name, value] of Object.entries(buildTokens(seed))) {
        expect(value.trim(), `${seed.id} ${name}`).not.toBe("");
        expect(value, `${seed.id} ${name}`).not.toContain("NaN");
      }
    }
  });

  it("keeps text legible against the surface it sits on", () => {
    for (const seed of THEME_SEEDS) {
      const tokens = buildTokens(seed);
      const canvas = luminance(seed.canvas);
      const ink = luminance(tokens["--el-text-color-primary"]);
      const muted = luminance(tokens["--el-text-color-secondary"]);
      // Not a WCAG assertion — these are published palettes and a few of them are
      // deliberately low-contrast. What this pins is the direction: primary text
      // must be further from the canvas than secondary text is, in whichever
      // direction the skin runs. A skin that got `ink` and `muted` the wrong way
      // round would look like a bug nobody could name.
      expect(Math.abs(ink - canvas), `${seed.id} ink`).toBeGreaterThan(Math.abs(muted - canvas));
      expect(Math.abs(ink - canvas), `${seed.id} contrast`).toBeGreaterThan(0.25);
    }
  });

  it("agrees with itself about which mode it is", () => {
    for (const seed of THEME_SEEDS) {
      const light = luminance(seed.canvas) > 0.5;
      expect(light, seed.id).toBe(seed.mode === "light");
      // The chrome is a step away from the canvas, never the same surface: that
      // step is what separates the sidebar from the conversation without a border.
      expect(seed.page, seed.id).not.toBe(seed.canvas);
      // Command output is dark in every skin, including the light ones, because
      // that is what output is written for.
      expect(luminance(seed.term.bg), `${seed.id} terminal`).toBeLessThan(0.2);
    }
  });

  it("gives the affirmative solid a readable label", () => {
    for (const seed of THEME_SEEDS) {
      const tokens = buildTokens(seed);
      const solid = luminance(tokens["--pg-solid"]);
      const label = luminance(tokens["--pg-on-solid"]);
      expect(Math.abs(solid - label), `${seed.id} solid`).toBeGreaterThan(0.35);
    }
  });
});
