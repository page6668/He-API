# Playground — UI Spec

应用 `knowledge/taste/design-system.md`(POV「宣纸·墨·朱砂印」,2026-07-11 批准)。
本文件是 `draft-story` 的 `UI Reference`。

## Screens

### Playground(`/{locale}/playground`)
- **purpose**:开发者选模型、调参数、发一条真实请求、看流式输出与估算读数,并导出等价调用代码。
- **requirement**:Story 10.6 AC1(已上线功能),本次仅重做视觉与版式。
- **触发本次重做的缺陷**:页面无最大宽度约束,元素在 1920px 上散落撑满,显得廉价(用户原话:"直接把屏幕都撑满显示了,看起来太丑")。
- **layout + hierarchy**:
  - 外层 `max-w-playground(1200px) mx-auto px-6 lg:px-8 py-10` —— **内容恒限宽居中**,治撑满屏。
  - 标题区左对齐(`text-h1` + `text-small text-ink-secondary`),不居中。
  - 主体 `grid lg:grid-cols-[480px_minmax(0,1fr)] gap-6 items-start`:
    - **左 480px 固定**:参数面板(单张卡)—— API key → 模型 → A/B → Temperature/Max tokens → System → User → 发送。
    - **右 流式区**:Output 卡 + Export 卡(纵向堆叠)。
  - 层级只靠 字重字号 / 1px 暖边框(`border-line`)/ surface 明度差。**静态表面零阴影**。
  - `<1024px` 自动单列堆叠(左在上)。

## Flows
选 key/模型 → 调参 → 输入 User message →「发送」→ Output 流式逐字 → 指标条出现(tokens/延迟/估算成本)→ 需要时切 Export 语言 → Copy 贴进自己的代码。
A/B 勾选后出现「模型 B」,发送改为非流式对比。深链 `#prefill=` 预填后给一条 notice。

## States
| 区域 | loading | empty | error |
|---|---|---|---|
| 发送 | 按钮文案 → `controls.sending`,禁用(opacity-40) | — | — |
| Output | 流式逐字追加(唯一动效) | `output.empty` 灰字(`text-ink-muted`)置于内嵌区 | `crimson/30` 描边 + `crimson/5` 底的**行内**提示,非满宽红 banner |
| 指标条 | 未发送时不渲染 | — | — |
| 无 API key | — | `ochre` 细描边提示条(安静,不喧宾夺主) | — |
| Export | — | — | — |

## System use
- **铁律1(数字即读数)**:Temperature / Max tokens 输入、tokens in/out、延迟 ms、估算成本 → `.tabular`(Plex Mono + tabular-nums)。模型 id 亦等宽(技术标识)。正文/标签不用等宽。
- **铁律2(朱砂只落一处)**:全屏唯一 `bg-seal` = 「发送」。Export 激活 tab 刻意用 `bg-ink text-paper` 而非朱砂;focus ring 用 `ring-seal/15`(瞬态,不算常驻元素)。
- **色**:`bg-paper` 页面底 / `bg-surface` 卡 / `bg-surface-sunken` 输出内嵌区 / `border-line` 分隔 / `bg-ink text-paper` 代码块。
- **形**:卡片 `rounded-lg(8)`,输入 `rounded-md(6)`。
- **动效**:仅 focus ring、hover 描边/底色、流式逐字;`duration-state(120ms)` + `ease-he`。无转场、无骨架闪烁。
- **memorable-thing 如何体现**:整页是一台仪表 —— 暖纸底、墨字、右侧读数区等宽对齐,一枚朱砂落在唯一的动作上。
- **i18n/RTL**:`dir` 由 locale 决定;数字/代码/模型 id 用 `<LtrText>` 做 LTR 岛;标签不做 uppercase/字距(中文禁用)。

## New patterns(候选回写 KB)
- `.tabular` 工具类(globals.css)—— 铁律1 的落地载体,已在 design-system.md 记录。
- `fieldCls / labelCls / panelCls` 三个局部样式常量:目前是本页局部;若 Models/Dashboard 落地时复用,应提为共享 `<Field>/<Panel>` 组件并回写 design-system(避免各页各写一套)。
