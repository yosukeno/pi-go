import { computed, ref } from "vue";
import { buildTokens } from "./build";
import type { ThemeSeed, Tokens } from "./build";
import { DEFAULT_DARK, DEFAULT_LIGHT, THEME_SEEDS } from "./themes";

export type { ThemeSeed } from "./build";

const STORAGE_KEY = "pi-go:theme";

/**
 * The skins, built once at module load.
 *
 * Eagerly rather than on demand: the whole set is about six kilobytes of derived
 * strings, and building them up front means a bad hex in a palette throws at boot
 * — where it is one stack trace — instead of the first time somebody picks that
 * skin.
 */
export interface Theme extends ThemeSeed {
  tokens: Tokens;
}

export const THEMES: Theme[] = THEME_SEEDS.map((seed) => ({ ...seed, tokens: buildTokens(seed) }));

const byId = new Map(THEMES.map((t) => [t.id, t]));

function prefersDark(): boolean {
  return typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches;
}

function stored(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY);
  } catch {
    // Private mode; the choice is then session-only.
    return null;
  }
}

/**
 * The skin in effect. A ref rather than a plain variable so a component can watch
 * it — the two consumers that cannot read CSS variables (the mermaid runtime and
 * xterm) need to be told when it changes.
 */
export const currentThemeId = ref<string>(resolveInitial());
export const currentTheme = computed(() => byId.get(currentThemeId.value) ?? THEMES[0]);
export const isDark = computed(() => currentTheme.value.mode === "dark");

/**
 * themeVersion counts applications, including re-applying the same skin.
 *
 * It exists for the JS-side consumers: a diagram already rendered into SVG carries
 * its colours inline, so it has to be re-rendered rather than re-styled, and
 * watching the id would miss nothing today but would silently stop working the
 * first time a skin is edited in place.
 */
export const themeVersion = ref(0);

function resolveInitial(): string {
  const saved = stored();
  if (saved && byId.has(saved)) return saved;
  return prefersDark() ? DEFAULT_DARK : DEFAULT_LIGHT;
}

/**
 * applyTheme writes the skin onto the document element.
 *
 * Inline custom properties rather than a stylesheet swap: they win over the :root
 * block in styles.scss without a specificity contest, they cost one pass over a
 * few dozen strings, and there is no moment where two skins are both live. The
 * SCSS block stays as the pre-script paint — see the comment there.
 *
 * Three things ride along with the tokens because they are not tokens:
 *
 *   data-theme    so a rule can key off a specific skin if one ever needs it,
 *                 and so the choice is visible in devtools.
 *   class="dark"  Element Plus' own dark-mode hook: its dark stylesheet (imported
 *                 in main.ts) is scoped to html.dark, which is what supplies the
 *                 handful of framework tokens no skin declares. Our inline
 *                 properties still win wherever both set the same one.
 *   color-scheme  so form controls, the caret and any native scrollbar the
 *                 -webkit- rules do not reach follow the skin.
 */
export function applyTheme(id: string, persist = true): void {
  const theme = byId.get(id) ?? THEMES[0];
  const root = document.documentElement;

  for (const [name, value] of Object.entries(theme.tokens)) {
    root.style.setProperty(name, value);
  }
  root.dataset.theme = theme.id;
  root.classList.toggle("dark", theme.mode === "dark");
  root.style.colorScheme = theme.mode;

  currentThemeId.value = theme.id;
  themeVersion.value++;

  // persist is false for the two paths that are *following* rather than choosing:
  // the boot paint and the system's dark-mode flip. Writing the key there would
  // record a choice nobody made, and the record is exactly what stops the
  // following — one visit in dark would pin the skin forever.
  if (!persist) return;
  try {
    localStorage.setItem(STORAGE_KEY, theme.id);
  } catch {
    // Private mode — session-only, same as the locale.
  }
}

/**
 * initTheme runs before the app mounts, so the first frame is already in the right
 * skin. It also follows the system while nothing has been chosen explicitly: a
 * laptop that flips to dark at sunset should not leave a light skin behind on a
 * tab that has never been told otherwise.
 */
export function initTheme(): void {
  applyTheme(resolveInitial(), false);
  if (typeof matchMedia !== "function") return;
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", (e) => {
    if (stored()) return;
    applyTheme(e.matches ? DEFAULT_DARK : DEFAULT_LIGHT, false);
  });
}

/** cssVar reads a token off the document, for the JS consumers that need a real
 * colour value rather than a var() reference. */
export function cssVar(name: string, fallback = ""): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback;
}
