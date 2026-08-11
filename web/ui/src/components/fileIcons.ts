import raw from "@iconify-json/vscode-icons/icons.json?raw";

// Per-file-type icons for the workspace panel, backed by the vscode-icons
// set (Iconify data, MIT). The whole 3.6MB collection ships as a raw string
// and is parsed once at module load — importing it as a JSON module instead
// would make vue-tsc crawl (literal type for 1500+ icons) and tree-shaking
// per-icon is impossible for a lookup table driven by runtime filenames.

export interface FileIcon {
  body: string;
  width: number;
  height: number;
}

interface IconifyJSON {
  icons: Record<string, { body: string; width?: number; height?: number }>;
  width?: number;
  height?: number;
}

const data = JSON.parse(raw) as IconifyJSON;
const SET_W = data.width ?? 24;
const SET_H = data.height ?? 24;

function icon(name: string): FileIcon | null {
  const i = data.icons[name];
  if (!i) return null;
  return { body: i.body, width: i.width ?? SET_W, height: i.height ?? SET_H };
}

const DEFAULT_FILE = icon("default-file")!;

// Yellow folders (flat-color-icons, MIT) — inlined, only two icons needed.
// The user asked for the classic yellow folder instead of vscode-icons' tan.
export const folderIcon: FileIcon = {
  body:
    '<path fill="#FFA000" d="M40 12H22l-4-4H8c-2.2 0-4 1.8-4 4v8h40v-4c0-2.2-1.8-4-4-4"/>' +
    '<path fill="#FFCA28" d="M40 12H8c-2.2 0-4 1.8-4 4v20c0 2.2 1.8 4 4 4h32c2.2 0 4-1.8 4-4V16c0-2.2-1.8-4-4-4"/>',
  width: 48,
  height: 48,
};
export const folderOpenIcon: FileIcon = {
  body:
    '<path fill="#FFA000" d="M38 12H22l-4-4H8c-2.2 0-4 1.8-4 4v24c0 1.1.9 2 2 2h31c1.7 0 3-1.3 3-3V16c0-2.2-1.8-4-4-4"/>' +
    '<path fill="#FFCA28" d="M42.2 18H15.3c-1.9 0-3.6 1.4-3.9 3.3L8 40h31.7c1.9 0 3.6-1.4 3.9-3.3l2.5-14c.5-2.4-1.4-4.7-3.9-4.7"/>',
  width: 48,
  height: 48,
};
// Same yellow folder with a green plus badge, for "new folder" affordances.
export const folderPlusIcon: FileIcon = {
  body:
    '<path fill="#FFA000" d="M40 12H22l-4-4H8c-2.2 0-4 1.8-4 4v8h40v-4c0-2.2-1.8-4-4-4"/>' +
    '<path fill="#FFCA28" d="M40 12H8c-2.2 0-4 1.8-4 4v20c0 2.2 1.8 4 4 4h32c2.2 0 4-1.8 4-4V16c0-2.2-1.8-4-4-4"/>' +
    '<circle cx="36" cy="35" r="10" fill="#43A047"/>' +
    '<path d="M36 30.5v9M31.5 35h9" stroke="#fff" stroke-width="2.6" stroke-linecap="round"/>',
  width: 48,
  height: 48,
};

// Terminal window icon (参考资料/shell.svg): a window with traffic lights and
// a >_ prompt. fill=currentColor so it follows the button's state colors.
export const terminalIcon: FileIcon = {
  body:
    '<path fill="currentColor" d="M951.099338 72.900662H72.900662A73.465784 73.465784 0 0 0 0 146.366446v731.267108a73.465784 73.465784 0 0 0 72.900662 73.465784h878.198676a73.465784 73.465784 0 0 0 72.900662-73.465784V146.366446a73.465784 73.465784 0 0 0-72.900662-73.465784z m-110.198676 73.465784a36.732892 36.732892 0 1 1-36.16777 36.732892 36.732892 36.732892 0 0 1 36.16777-36.732892z m-145.801324 0a36.732892 36.732892 0 1 1-36.732892 36.732892 36.732892 36.732892 0 0 1 36.732892-36.732892z m-146.366446 0a36.732892 36.732892 0 1 1-36.732892 36.732892 36.732892 36.732892 0 0 1 36.732892-36.732892z m406.322296 697.924945a38.993377 38.993377 0 0 1-38.993378 38.993377H106.807947a38.993377 38.993377 0 0 1-38.993377-38.993377V293.863135h887.240618zM202.313466 791.169978a36.16777 36.16777 0 0 0 51.99117 0l182.534216-182.534216a37.298013 37.298013 0 0 0 0-51.99117L254.304636 374.110375a36.732892 36.732892 0 0 0-53.121413 50.295806l157.103753 156.538631-155.97351 158.799117a36.732892 36.732892 0 0 0 0 51.426049z m258.825607-25.430464a36.732892 36.732892 0 0 0 36.732892 36.167771H791.169978a36.732892 36.732892 0 1 0 0-72.900662H497.871965a36.732892 36.732892 0 0 0-36.732892 36.732891z"/>',
  width: 1024,
  height: 1024,
};

// Chat bubble with text lines (参考资料/message.svg): marks the message a
// dialog is about. fill=currentColor so it follows the context's color.
export const messageIcon: FileIcon = {
  body:
    '<path fill="currentColor" d="M362.666667 640a32 32 0 0 1 0-64h170.666666a32 32 0 0 1 0 64h-170.666666z m0-192a32 32 0 0 1 0-64h298.666666a32 32 0 0 1 0 64H362.666667z m455.786666 390.784l28.096 60.586667A42.666667 42.666667 0 0 1 807.850667 960H512C264.576 960 64 759.424 64 512S264.576 64 512 64s448 200.576 448 448a446.762667 446.762667 0 0 1-141.546667 326.784zM512 896h262.442667l-14.037334-30.293333a64 64 0 0 1 14.272-73.6A382.698667 382.698667 0 0 0 896 512c0-212.074667-171.925333-384-384-384S128 299.925333 128 512s171.925333 384 384 384z"/>',
  width: 1024,
  height: 1024,
};

// Two overlapping session windows (参考资料/session-state.svg): the session
// delete dialog's header mark. fill=currentColor as well.
export const sessionIcon: FileIcon = {
  body:
    '<path fill="currentColor" d="M968.992 499.232h-49.696c-15.04 24.992-60.64 97.856-63.808 103.776-3.456 6.208-11.424 6.368-14.88 0-4.448-8.256-49.056-79.84-63.68-103.776H374.944a54.56 54.56 0 0 1-54.848-54.304V54.304c0-29.92 24.576-54.304 54.848-54.304h594.08c30.432 0 55.008 24.352 55.008 54.304v390.624a54.72 54.72 0 0 1-55.008 54.304z m21.984-444.96a21.888 21.888 0 0 0-22.016-21.664H374.976a21.92 21.92 0 0 0-21.952 21.664v390.688c0 12 9.952 21.664 21.952 21.664h594.08a21.824 21.824 0 0 0 22.016-21.664V54.272h-0.064zM480 288.096h400v48H480v-48z m0-144.032h400v48H480v-48zM48 208.032v576a32 32 0 0 0 32.064 31.968h704a31.936 31.936 0 0 0 31.904-31.968v-111.968h48.064v111.968a80 80 0 0 1-80 80H711.68c-21.824 36.768-88.032 144.224-92.768 153.056-4.864 9.12-16.672 9.248-21.6 0-6.432-12-71.456-117.76-92.704-153.056H80.032a80 80 0 0 1-80.064-80v-576c0-44.192 35.872-80 80.064-80h143.936v48H80.032a32 32 0 0 0-32.064 31.968z"/>',
  width: 1024,
  height: 1024,
};

// Exact lowercase filenames that the extension alone can't identify.
const SPECIAL_NAMES: Record<string, string> = {
  dockerfile: "docker",
  containerfile: "docker",
  ".dockerignore": "docker",
  "docker-compose.yml": "docker",
  "docker-compose.yaml": "docker",
  "compose.yml": "docker",
  "compose.yaml": "docker",
  "go.mod": "go-package",
  "go.sum": "go-package",
  "go.work": "go-work",
  "go.work.sum": "go-work",
  "package.json": "npm",
  "package-lock.json": "npm",
  "npm-shrinkwrap.json": "npm",
  "yarn.lock": "yarn",
  ".yarnrc": "yarn",
  ".yarnrc.yml": "yarn",
  "pnpm-lock.yaml": "pnpm",
  "pnpm-workspace.yaml": "pnpm",
  "bun.lock": "bun",
  "bun.lockb": "bun",
  "deno.json": "deno",
  "deno.jsonc": "deno",
  "deno.lock": "deno",
  "cmakelists.txt": "cmake",
  ".gitignore": "git",
  ".gitattributes": "git",
  ".gitmodules": "git",
  ".gitkeep": "git",
  ".mailmap": "git",
  license: "license",
  "license.md": "license",
  "license.txt": "license",
  licence: "license",
  "licence.md": "license",
  copying: "license",
  notice: "license",
  "claude.md": "claude",
  ".editorconfig": "editorconfig",
  ".browserslistrc": "browserslist",
  browserslist: "browserslist",
  "schema.prisma": "prisma",
};

// Substring rules for config files with many spelling variants.
const NAME_CONTAINS: Array<[string, string]> = [
  ["prettier", "prettier"],
  ["eslint", "eslint"],
  ["babel", "babel"],
  ["tailwind", "tailwind"],
  ["postcss", "postcss"],
  ["jest.config", "jest"],
  ["vitest.config", "vitest"],
  ["vitest.workspace", "vitest"],
  ["vite.config", "vite"],
  ["rollup.config", "rollup"],
  ["webpack.config", "webpack"],
];

// Extensions whose icon name differs from the extension itself.
const EXT_ALIAS: Record<string, string> = {
  ts: "typescript",
  mts: "typescript",
  cts: "typescript",
  tsx: "reactts",
  mjs: "js",
  cjs: "js",
  jsx: "reactjs",
  py: "python",
  pyi: "python",
  pyw: "python",
  md: "markdown",
  markdown: "markdown",
  yml: "yaml",
  sh: "shell",
  bash: "shell",
  zsh: "shell",
  fish: "shell",
  ksh: "shell",
  ps1: "powershell",
  psm1: "powershell",
  pdf: "pdf2",
  dart: "dartlang",
  csv: "excel",
  tsv: "excel",
  ipynb: "jupyter",
  cs: "csharp",
  csx: "csharp",
  fs: "fsharp",
  fsx: "fsharp",
  kt: "kotlin",
  kts: "kotlin",
  rb: "ruby",
  rs: "rust",
  ex: "elixir",
  exs: "elixir",
  erl: "erlang",
  hrl: "erlang",
  hs: "haskell",
  lhs: "haskell",
  clj: "clojure",
  cljs: "clojure",
  cljc: "clojure",
  pl: "perl",
  pm: "perl",
  r: "r",
  jl: "julia",
  cc: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  hh: "cpp",
  hxx: "cpp",
  h: "c",
  gql: "graphql",
  proto: "protobuf",
  tf: "terraform",
  tfvars: "terraform",
  mmd: "mermaid",
  txt: "text",
  log: "text",
  pem: "cert",
  crt: "cert",
  cer: "cert",
  der: "cert",
};

// Category fallbacks: one icon covers a whole family of extensions.
const EXT_GROUPS: Array<[string, string[]]> = [
  ["image", ["png", "jpg", "jpeg", "gif", "webp", "ico", "icns", "bmp", "avif", "tif", "tiff", "heic", "heif"]],
  ["audio", ["mp3", "wav", "ogg", "flac", "m4a", "aac", "opus", "mid", "midi"]],
  ["video", ["mp4", "mov", "mkv", "avi", "webm", "flv", "wmv", "m4v"]],
  ["zip", ["zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "zst", "lz4", "jar", "war"]],
  ["font", ["ttf", "otf", "woff", "woff2", "eot"]],
  ["binary", ["exe", "dll", "so", "dylib", "bin", "o", "a", "lib", "class", "apk", "dmg", "iso", "img", "msi", "com"]],
];

/** Resolve a workspace file name (base name, not a path) to its type icon. */
export function fileIcon(name: string): FileIcon {
  const lower = name.toLowerCase();

  const special = SPECIAL_NAMES[lower];
  if (special) return icon(`file-type-${special}`) ?? DEFAULT_FILE;

  if (lower === ".env" || lower.startsWith(".env.") || lower.endsWith(".env")) {
    return icon("file-type-dotenv") ?? DEFAULT_FILE;
  }
  for (const [sub, iconName] of NAME_CONTAINS) {
    if (lower.includes(sub)) return icon(`file-type-${iconName}`) ?? DEFAULT_FILE;
  }
  if (lower.startsWith("tsconfig") && lower.endsWith(".json")) {
    return icon("file-type-tsconfig") ?? DEFAULT_FILE;
  }
  if (lower.endsWith(".d.ts")) {
    return icon("file-type-typescript") ?? DEFAULT_FILE;
  }

  const dot = lower.lastIndexOf(".");
  if (dot > 0) {
    const ext = lower.slice(dot + 1);
    const alias = EXT_ALIAS[ext];
    if (alias) return icon(`file-type-${alias}`) ?? DEFAULT_FILE;
    const direct = icon(`file-type-${ext}`);
    if (direct) return direct;
    for (const [group, exts] of EXT_GROUPS) {
      if (exts.includes(ext)) return icon(`file-type-${group}`) ?? DEFAULT_FILE;
    }
  }
  return DEFAULT_FILE;
}

/** Base name of a workspace-relative path, for icon lookup. */
export function baseName(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? path : path.slice(i + 1);
}
