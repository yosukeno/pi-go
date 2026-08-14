# 皮肤系统调研与设计

pi-go Web UI 的多皮肤能力：14 套皮肤、一个切换器、一层派生式 token。本文是调研结论 + 落地方案 + 取舍记录。

代码位置：

| 文件 | 作用 |
|---|---|
| `web/ui/src/theme/color.ts` | 颜色运算（parse / mix / alpha / luminance / readable） |
| `web/ui/src/theme/build.ts` | `ThemeSeed` → 完整 token 集（约 70 个变量） |
| `web/ui/src/theme/themes.ts` | 14 套皮肤的种子色板 + 默认皮肤 |
| `web/ui/src/theme/index.ts` | 应用 / 持久化 / 跟随系统 / `themeVersion` |
| `web/ui/src/components/ThemePicker.vue` | 切换器（语言按钮旁） |
| `web/ui/src/styles.scss` | 结构性 token + 首帧兜底（Paper） |

---

## 一、调研：主流开发工具皮肤在做什么

### 1.1 两类不同的“主题”

调研中最关键的一条区分，直接决定了后面的方案形态：

| | 编辑器主题（Nord / Gruvbox / Catppuccin…） | 产品主题（ChatGPT / Claude / Linear） |
|---|---|---|
| 目标 | 让**代码语法**可读、久看不累 | 让**界面层级**清晰、品牌可识别 |
| 色板规模 | 15～26 色（含 ANSI 16 色语义槽） | 2～3 个中性面 + 1 个强调色 |
| 中性色 | 3～6 阶背景 + 3 阶前景 | 依赖阴影与留白，而非多阶灰 |
| 强调色 | 多个（每种语法一个） | 一个，且只用于“活着的状态” |

pi-go 的界面既有大段对话正文（产品形态），又有代码块、diff、终端输出（编辑器形态）。所以方案必须两者兼顾：**界面层级按产品主题的规矩来（少色、靠面区分），语法/终端按编辑器主题的规矩来（色板槽位齐全）**。

### 1.2 被广泛采用的色板（本次全部实地取值）

以下 hex 均取自上游官方来源，非二手整理：

| 家族 | 作者 | 取值来源 | 特征 |
|---|---|---|---|
| Catppuccin (Latte/Mocha) | 社区 | `catppuccin.com/palette` | 4 flavour × 26 色，语义命名规范（base/mantle/crust、text/subtext/overlay/surface），是当下最工程化的色板 |
| Rosé Pine (Base/Moon/Dawn) | Rosé Pine | `rosepinetheme.com/palette` | 15 色，无绿色；命名走意象（love/gold/rose/pine/foam/iris） |
| Everforest (Light/Dark) | sainnhe | `sainnhe/everforest` `palette.md` | 绿系护眼，背景分 hard/medium/soft 三档对比度 |
| Nord | Arctic Ice Studio | `nordtheme.com` | 16 色四组（Polar Night / Snow Storm / Frost / Aurora），冷蓝 |
| Tokyo Night | enkia / folke | `folke/tokyonight.nvim` | 深靛底 + 电蓝/紫霓虹 |
| Gruvbox | morhetz | `morhetz/gruvbox` | 复古暖棕 + 高饱和黄橙，soft/bright 双 ramp |
| Dracula | Dracula | draculatheme.com | 紫粉高饱和，辨识度最高 |
| Solarized | Ethan Schoonover | 官方定义 | 唯一按 CIELAB 精算的色板，base03…base3 亮度等距 |
| One Dark | Atom | Atom / One Dark Pro | 事实上的“默认深色” |
| Primer | GitHub | `primer/primitives` | canvas/fg/border/accent 语义分层，最中性 |

### 1.3 三个共同规律（方案直接采纳）

1. **背景至少两层。** 每个家族都区分“编辑区”和“侧栏/面板”（base/mantle、bg0/bg_dim、canvas.default/canvas.subtle）。层级不靠边框，靠面。
2. **强调色与语义色分离。** 语法高亮的红 ≠ 报错的红。Rosé Pine 没有绿，就不硬造一个绿。
3. **亮色版不是暗色版取反。** Solarized 和 Everforest 的亮/暗两版各自独立调过；机械反色一定翻车（对比度与色相偏移都不守恒）。

---

## 二、设计：为什么是“12 色种子 + 派生”

### 2.1 问题规模

界面真正消费的 token 约 70 个：6 阶 fill、7 阶 border、5 阶文字、5 组语义色 × 7 阶 ramp、阴影、focus ring、终端面、6 个语法色、4 个图表色。

14 套皮肤 × 70 = **约 1000 个色值**。手写的后果不是累，是**漂移**：某套皮肤的 hover 面与静息面差 4%，另一套差 9%，于是界面在部分皮肤里显得“有点坏”，而没人能指出坏在哪。

### 2.2 方案

每套皮肤只声明 12 个颜色 + 2 个面：

```ts
{ id, name, mode, origin,
  page, canvas,          // 外壳面 / 内容面
  ink, muted,            // 主文字 / 次文字
  accent, success, warning, danger, violet,
  solid?, term }         // 主操作实色 / 终端面
```

其余全部由 `buildTokens()` 用统一比例派生：

```
fill      canvas → ink   2 / 4 / 6 / 9 / 13 / 18 %
border    canvas → ink   9 / 14 / 20 / 28 / 34 / 42 / 50 %
text      ink · ink→canvas 15% · muted · muted→canvas 30% · 50%
ramp      color → canvas 30 / 50 / 70 / 80 / 90 %   (Element 的 light-3…light-9)
```

**关键一点：所有派生都朝 `canvas` 混，不朝白色混。** 这是一条公式同时服务亮/暗皮肤的原因——在暗底上，同样的 4% 混合得到的是“抬升一层的面”，而不是被冲淡的灰。Element Plus 的 `light-N` 官方语义是“向白色靠 N/10”，在亮色皮肤下二者等价，在暗色皮肤下只有朝 canvas 混才讲得通。

单元测试 `theme.test.ts` 钉住了这套派生的三条不变量：每套皮肤 token 集合完全一致（不可能漏 token）、主文字比次文字离 canvas 更远、`mode` 与 canvas 实测亮度一致。

### 2.3 沿用 sRGB 而非 OKLCH

OKLCH 混得更均匀，但这 12 套色板全部是作者在 sRGB 里手调的。用感知空间插值会把派生台阶推离家族自身的 ramp——目标是“像 Nord”，不是“像 Nord 的正确插值”。唯一需要客观判断的地方（实色按钮上放什么颜色的字）用 WCAG 相对亮度算，那本来就是 sRGB 定义。

---

## 三、14 套皮肤

亮色 6 套：

| id | 名称 | 来源 | 取值要点 |
|---|---|---|---|
| `paper` | Paper | pi-go | 暖纸 + 陶土强调色，默认皮肤 |
| `primer-light` | Primer Light | GitHub Primer | 最中性；截图不想带风格时用它 |
| `latte` | Catppuccin Latte | Catppuccin | base 作内容面、mantle 作外壳；text/subtext0 |
| `rose-pine-dawn` | Rosé Pine Dawn | Rosé Pine | Iris 作强调色（Love 必须留给“危险”） |
| `everforest-light` | Everforest Light | sainnhe | medium 档；绿色是家族签名，占强调位 |
| `solarized-light` | Solarized Light | Schoonover | base3/base2/base01/base1 原样 |

暗色 8 套：

| id | 名称 | 来源 | 取值要点 |
|---|---|---|---|
| `graphite` | Graphite | pi-go | Paper 的夜面，系统偏好暗色时的默认 |
| `mocha` | Catppuccin Mocha | Catppuccin | Mauve 强调，crust 作终端面 |
| `tokyo-night` | Tokyo Night | enkia/folke | muted 抬离 `comment` |
| `nord` | Nord | Arctic Ice Studio | nord0 作内容面，外壳压暗一档 |
| `gruvbox-dark` | Gruvbox Dark | morhetz | dark0 / dark0_hard，bright ramp |
| `rose-pine` | Rosé Pine | Rosé Pine | base 内容面、surface 外壳 |
| `dracula` | Dracula | Dracula | warning 用 orange，非 `#f1fa8c` |
| `one-dark` | One Dark | Atom | ink 比 `#abb2bf` 更亮 |
| `primer-dark` | Primer Dark | GitHub Primer | canvas.default / subtle / inset |

### 三处刻意偏离原色板（都是因为“界面 ≠ 编辑器”）

1. **次要文字不用 comment 色。** Tokyo Night `#565f89`、Dracula `#6272a4` 是为“退到代码后面”调的；同一个色值放到 UI 标签上就不是安静而是读不出来。两者都抬亮了。
2. **强调色不占用语义红。** Rosé Pine 的 Love 是家族红，而界面需要一个稳定表示“销毁性操作”的红，所以 Dawn/Base 都改用 Iris 作强调色。同理 Rosé Pine 没有绿，success 由 Foam（青）承担，而不是外借一个绿。
3. **One Dark 的 ink 提亮。** `#abb2bf` 是小字号代码前景，作为正文主色低于段落所需对比度。

这些都在 `themes.ts` 的注释里逐条写明了原因。

---

## 四、跨皮肤的四个硬骨头

派生 token 解决的是 CSS 消费者。真正的风险在**不读 CSS 变量**的地方——它们在暗色皮肤下会原形毕露。

| 位置 | 原状 | 处理 |
|---|---|---|
| 终端输出块（CodeBlock / ToolCall / ShellPanel） | 硬编码 `#1e1f22` | `--pg-term-bg/fg/line/fill`；**每套皮肤都是暗的**，亮色皮肤借用本家族的暗色面（Solarized Light 的输出块就是 Solarized Dark，Latte 借 Mocha） |
| 语法高亮 6 色 | 硬编码 GitHub 亮色系 | 映射到皮肤已声明的语义色（string→success、keyword→danger、type→violet、func→warning、number→accent、comment→muted），亮色皮肤再朝 ink 压 12% |
| xterm.js | canvas 渲染，读不到 CSS 变量 | `cssVar()` 取真实色值传入；`watch(themeVersion)` 原地换主题，**保留 scrollback** |
| Mermaid | 颜色被写进产出的 SVG | themeVariables 全部取自 token；换皮肤时 `resetMermaidTheme()` + 重新 render；暗色皮肤下跳过“提亮深色填充”那一步（否则每个节点会被漂白成浮在暗底上的浅块） |

另外 ANSI 16 色保持固定，不跟皮肤走：那是**程序自己输出的语义色**，皮肤改写它就等于改写了程序说的话，而不是改外观。所有终端面都是暗的，这套 ramp 在每套皮肤下都可读。

---

## 五、切换器与状态

- 位置：侧栏底部，**语言按钮左侧**。两者都是“关于这个窗口”的偏好，而非会话事实，所以放在一起；侧栏收起时在图标栏里同样紧邻语言图标。
- 按钮本身就是当前皮肤的三色样本（外壳 / 内容 / 强调），而不是一个油漆桶图标——`Nord` 这个词在你见过它之前没有信息量。
- 列表按亮/暗分组，每行三色样本 + 名称 + **来源署名**（Catppuccin、morhetz、GitHub Primer…）。借来的色板在 UI 里署名，成本为零。
- 持久化：`localStorage["pi-go:theme"]`。
- 首次访问跟随 `prefers-color-scheme`（亮 → Paper，暗 → Graphite），并且**在用户没有明确选择之前持续跟随**系统切换。`applyTheme(id, persist)` 的第二个参数就是为此存在：跟随路径不写 localStorage，否则“跟随”会被自己第一次跟随的结果永久钉死。
- 应用时机：`main.ts` 里 mount 之前。若放到组件 `onMounted`，暗色皮肤会先闪一帧亮色。

---

## 六、已知边界

1. **`styles.scss` 里的 Paper 兜底与 `themes.ts` 的 Paper 种子重复。** 前者是脚本执行前的首帧兜底，两份值需要一起改。可以在构建期生成消除重复，但为一份 30 行的兜底引入构建步骤不划算。
2. **已渲染的 Mermaid 图**在换皮肤时会重新 render（有一次闪烁），这是 SVG 内联颜色的必然结果。
3. **对比度未做 WCAG 断言。** 部分原色板（Solarized、Rosé Pine）本身就是刻意低对比的，硬性断言会把它们判死。测试只钉方向性不变量（主文字必须比次文字离底更远等）。
4. **暗色皮肤下 diff 的加/减底色**沿用 `--el-color-success/danger-light-9`，在暗色下是很暗的 tint——可读，但不如专门调的 diff 色。真要做，需要给种子加 `diffAdd/diffDel` 两个字段。

---

## 七、加一套皮肤要做什么

往 `THEME_SEEDS` 里加一条 12 色种子即可，无需碰任何组件：

```ts
{
  id: "kanagawa", name: "Kanagawa", mode: "dark", origin: "rebelot",
  page: "#16161d", canvas: "#1f1f28",
  ink: "#dcd7ba", muted: "#9a9a8f",
  accent: "#7e9cd8", success: "#98bb6c", warning: "#ffa066",
  danger: "#e46876", violet: "#957fb8",
  term: { bg: "#16161d", fg: "#dcd7ba" },
}
```

`theme.test.ts` 会自动把它纳入全部不变量检查（token 完整性、模式与亮度一致、终端面必须够暗、实色按钮标签必须可读）。
