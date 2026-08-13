import { describe, expect, it } from "vitest";
import { hubTenants, migrateSheet, resolveTenant, type Tenant } from "./dockSheets";

// The migration is the part that can silently ruin a returning user's layout,
// so every id any shipped build could have written gets a case.
describe("migrateSheet", () => {
  it("keeps the two current sheets untouched", () => {
    expect(migrateSheet({ sheet: "files" })).toEqual({ sheet: "files", tenant: null });
    expect(migrateSheet({ sheet: "hub" })).toEqual({ sheet: "hub", tenant: null });
  });

  it("moves the pre-hub shell sheet into the hub", () => {
    expect(migrateSheet({ sheet: "shell" })).toEqual({ sheet: "hub", tenant: "shell" });
  });

  it("moves a pre-hub panel sheet into the hub, keeping which panel", () => {
    expect(migrateSheet({ sheet: "panel:样本库" })).toEqual({
      sheet: "hub",
      tenant: "panel:样本库",
    });
  });

  it("migrates the pre-sheet booleans", () => {
    expect(migrateSheet({ sheet: null, legacyFiles: true })).toEqual({
      sheet: "files",
      tenant: null,
    });
    expect(migrateSheet({ sheet: null, legacyShell: true })).toEqual({
      sheet: "hub",
      tenant: "shell",
    });
  });

  it("prefers the sheet id over the legacy booleans", () => {
    expect(migrateSheet({ sheet: "files", legacyShell: true }).sheet).toBe("files");
  });

  it("opens nothing for an unknown id or a clean slate", () => {
    expect(migrateSheet({ sheet: "from-a-newer-build" })).toEqual({ sheet: null, tenant: null });
    expect(migrateSheet({ sheet: null })).toEqual({ sheet: null, tenant: null });
  });

  it("is idempotent: migrating its own output changes nothing", () => {
    const once = migrateSheet({ sheet: "shell" });
    expect(migrateSheet({ sheet: once.sheet })).toEqual({ sheet: "hub", tenant: null });
  });
});

describe("hubTenants", () => {
  it("puts the shell first and keeps the server's panel order", () => {
    const tenants = hubTenants(
      [
        { name: "样本库", path: "/panels/样本库/" },
        { name: "grafana", path: "/panels/grafana/" },
      ],
      "Shell",
    );
    expect(tenants.map((t) => t.id)).toEqual(["shell", "panel:样本库", "panel:grafana"]);
    expect(tenants[1]).toMatchObject({ kind: "panel", label: "样本库", path: "/panels/样本库/" });
  });

  it("still offers the shell when no panels are registered", () => {
    expect(hubTenants([]).map((t) => t.id)).toEqual(["shell"]);
  });
});

describe("resolveTenant", () => {
  const tenants: Tenant[] = hubTenants([{ name: "样本库", path: "/panels/样本库/" }]);

  it("returns the remembered tenant", () => {
    expect(resolveTenant(tenants, "panel:样本库")?.label).toBe("样本库");
  });

  it("falls back to the first tenant when the remembered panel is gone", () => {
    expect(resolveTenant(tenants, "panel:removed")?.id).toBe("shell");
    expect(resolveTenant(tenants, null)?.id).toBe("shell");
  });

  it("returns null only when there is nothing to show", () => {
    expect(resolveTenant([], "shell")).toBeNull();
  });
});
