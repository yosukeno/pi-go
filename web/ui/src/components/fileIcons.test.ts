import { describe, expect, it } from "vitest";
import { baseName, fileIcon, folderIcon, folderOpenIcon } from "./fileIcons";

// Distinct bodies prove a real type icon was resolved; equal-to-default
// proves a fallback. This catches silent regressions in the lookup order.

const body = (name: string) => fileIcon(name).body;
const DEFAULT = fileIcon("no-such-ext.zzzqqq").body;

describe("fileIcon", () => {
  it("maps common extensions to distinct, non-default icons", () => {
    const names = ["main.go", "app.ts", "x.py", "a.rs", "b.java", "c.vue", "d.rb", "e.php"];
    const bodies = new Set(names.map(body));
    expect(bodies.size).toBe(names.length);
    for (const b of bodies) expect(b).not.toBe(DEFAULT);
  });

  it("aliases extensions whose icon name differs", () => {
    expect(body("a.ts")).not.toBe(body("a.tsx")); // typescript vs reactts
    expect(body("a.yml")).toBe(body("a.yaml"));
    expect(body("a.mjs")).toBe(body("b.js"));
    expect(body("a.pdf")).not.toBe(DEFAULT);
    expect(body("a.dart")).not.toBe(DEFAULT);
    expect(body("a.ipynb")).not.toBe(DEFAULT);
  });

  it("recognizes special filenames", () => {
    for (const n of ["Dockerfile", "go.mod", "go.sum", ".gitignore", ".env.local", "LICENSE", "package-lock.json"]) {
      expect(body(n), n).not.toBe(DEFAULT);
    }
    expect(body("tsconfig.json")).not.toBe(body("plain.json"));
    expect(body("vite.config.ts")).not.toBe(body("app.ts"));
    expect(body("go.mod")).not.toBe(body("go.md"));
  });

  it("groups media/archive/font/binary extensions", () => {
    expect(body("a.png")).toBe(body("b.jpeg"));
    expect(body("a.mp3")).toBe(body("b.flac"));
    expect(body("a.tar")).toBe(body("b.7z"));
    expect(body("a.ttf")).toBe(body("b.woff2"));
    expect(body("a.dylib")).toBe(body("b.exe"));
    expect(body("a.png")).not.toBe(DEFAULT);
  });

  it("falls back to default-file for unknown extensions and dotfiles", () => {
    expect(DEFAULT.length).toBeGreaterThan(0);
    expect(body(".DS_Store")).toBe(DEFAULT);
    expect(body("Makefile")).toBe(DEFAULT);
  });

  it("folders are the yellow flat icons", () => {
    expect(folderIcon.body).toContain("#FFCA28");
    expect(folderOpenIcon.body).toContain("#FFCA28");
    expect(folderIcon.body).not.toBe(folderOpenIcon.body);
  });

  it("baseName strips directories", () => {
    expect(baseName("a/b/c.go")).toBe("c.go");
    expect(baseName("c.go")).toBe("c.go");
    expect(baseName("dir/")).toBe("");
  });
});
