import type { PanelInfo } from "@/api/types";

// The dock has exactly two sheets, because the rail is chrome: it carries the
// runtime's own affordances, never domain content. Growing it by one icon per
// registered -web-panel put a data application next to "files" and "shell",
// which are properties of the running agent — the mismatch is what read as
// out of place.
//
// So: "files" keeps its own button (high traffic, and it is workspace state
// read alongside the conversation), and everything else becomes a tenant of
// "hub" — one visible at a time, switched from a menu in the hub's own header.
// That is the browser side-panel model rather than the VS Code activity-bar
// model: a single region, a dropdown to choose the occupant. Registering more
// panels can no longer change the rail.
export type SheetId = "files" | "hub";

/** TenantId is "shell" for the built-in terminal, "panel:<name>" for a -web-panel. */
export type TenantId = string;

export const PANEL_PREFIX = "panel:";

export interface Tenant {
  id: TenantId;
  label: string;
  kind: "shell" | "panel";
  /** Reverse-proxied path (/panels/<name>/); iframe tenants only. */
  path?: string;
}

export const SHEET_KEY = "pi-go:active-sheet";
export const TENANT_KEY = "pi-go:hub-tenant";

export interface StoredSheet {
  /** SHEET_KEY. Pre-hub builds wrote "shell" or "panel:<name>" here. */
  sheet: string | null;
  /** Pre-sheet builds: "pi-go:files-open". */
  legacyFiles?: boolean;
  /** Pre-sheet builds: "pi-go:shell-open". */
  legacyShell?: boolean;
}

export interface SheetState {
  sheet: SheetId | null;
  /** Set only when the stored id carried a tenant; null means "keep TENANT_KEY". */
  tenant: TenantId | null;
}

/**
 * migrateSheet folds every id previous builds could have persisted onto the
 * two-sheet rail. It runs on every boot rather than once, which is what makes
 * it safe: the mapping is idempotent, so a downgrade-then-upgrade cannot strand
 * someone on a sheet that no longer exists.
 *
 * An id from a *newer* build is the one case that cannot be mapped, and it
 * opens nothing — a closed dock is recoverable from the rail, whereas guessing
 * could open a sheet the user never chose.
 */
export function migrateSheet(stored: StoredSheet): SheetState {
  const v = stored.sheet;
  if (v === "files" || v === "hub") return { sheet: v, tenant: null };
  if (v === "shell") return { sheet: "hub", tenant: "shell" };
  if (v && v.startsWith(PANEL_PREFIX)) return { sheet: "hub", tenant: v };
  if (v) return { sheet: null, tenant: null };
  if (stored.legacyFiles) return { sheet: "files", tenant: null };
  if (stored.legacyShell) return { sheet: "hub", tenant: "shell" };
  return { sheet: null, tenant: null };
}

/**
 * hubTenants lists the hub's occupants in display order: the built-in shell
 * first, then panels in the order the server registered them. The shell label
 * is passed in so this module stays free of i18n and therefore testable as
 * plain data.
 */
export function hubTenants(panels: PanelInfo[], shellLabel = "Shell"): Tenant[] {
  const out: Tenant[] = [{ id: "shell", label: shellLabel, kind: "shell" }];
  for (const p of panels) {
    out.push({ id: PANEL_PREFIX + p.name, label: p.name, kind: "panel", path: p.path });
  }
  return out;
}

/**
 * resolveTenant picks the occupant to render. A remembered tenant that no
 * longer exists — a -web-panel dropped from the command line since last visit
 * — falls back to the first one instead of leaving the hub blank, because an
 * empty container with no explanation reads as a broken panel.
 */
export function resolveTenant(tenants: Tenant[], wanted: TenantId | null): Tenant | null {
  if (tenants.length === 0) return null;
  return tenants.find((t) => t.id === wanted) ?? tenants[0];
}
