# taste/design-system

The durable visual system `design-ui` applies. Authored by the `design-system`
skill. Structured, with provenance. Specifics, not adjectives.

```yaml
memorable_thing: "一台为中国大模型调校过的仪表——宣纸底、墨字、一枚朱砂印;每个数字都对齐、可读、可比。"

references: # 2-3 named products this steals direction from (researched live, 2026-07-11)
  - name: OpenRouter
    take: "分面筛选+密集列表的模型市场(非卡片墙);价格前置(¥/M in·out 与上下文同排);导航内 ⌘K 搜索;内容恒有最大宽度。"
    leave: "紫色渐变调性(已被其占有);居中堆叠的营销页。"
  - name: Linear
    take: "极度克制——无渐变、无卡片墙、无处不在的阴影;左对齐编辑式标题;直接展示产品本体而非抽象插画;等宽字标记技术值。"
    leave: "纯黑主题(He-API 走浅色宣纸,与其区隔)。"
  - name: Stripe
    take: "企业级密度——密而不挤的数据排版;语义色极克制;1px 分隔线而非阴影建立层级。"
    leave: "靛紫主色。"

distinctive_rule: >
  两条铁律,违反即不是 He-API:
  (1) 数字即仪表读数——所有数值(上下文长度/单价/延迟/百分比/Token 数)一律 IBM Plex Mono +
      font-variant-numeric: tabular-nums,与正文字体分离且右对齐可比;正文永不用等宽。
  (2) 朱砂只落一处——每屏 accent(朱砂红)最多出现在一个位置:主操作按钮 或 当前激活态,
      像一枚印章。其余一律墨色/灰阶。accent 永不用于装饰、渐变、大面积填充。

typography:
  typeface: >
    拉丁/数字:IBM Plex Sans + IBM Plex Mono(自托管,subset 拉丁+数字,约 100KB 内)。
    中文:系统字体栈 PingFang SC → Microsoft YaHei → Noto Sans CJK SC → sans-serif(零下载)。
    明确不用 Inter/Roboto。理由:①IBM Plex 的工程 humanist 气质契合"基础设施仪表"定位,
    区别于通用 SaaS 的圆润感;②开源可自托管——大陆访问 Google Fonts 不可靠,自托管是硬需求;
    ③**刻意不自托管 Plex Sans SC**:CJK 全量字重 10MB+,会毁掉首屏;中文交给系统栈在各端
    (PingFang/雅黑)本就原生好看,且与 Plex 的中性 grotesk 骨架不冲突。
    个性由「拉丁+数字」承担,中文只需清晰——这是双语产品的务实解,不是妥协。
  scale: # px/line-height/weight/tracking
    - "display 44/1.10/600/-0.02em — 营销页主标题(左对齐,不居中)"
    - "h1 30/1.25/600/-0.01em — 页面标题"
    - "h2 22/1.30/600/0 — 区块标题"
    - "h3 17/1.40/600/0 — 卡片/组标题"
    - "body 15/1.60/400/0 — 正文(双语舒适基准)"
    - "small 13/1.50/400/0 — 次要说明"
    - "label 12/1.40/500/+0.02em — 标签(仅拉丁可 uppercase;中文禁用大写与字距)"
    - "metric-lg 28/1.10/600 Plex Mono tabular — 仪表大数(仪表盘/统计)"
    - "metric 15/1.40/500 Plex Mono tabular — 行内数值(上下文/单价)"
    - "code 13/1.60/400 Plex Mono — 代码/模型 id"
  line_height: >
    中文行高比拉丁 +0.1(body 拉丁 1.5 → 中文 1.6);中文禁用 italic、禁用 letter-spacing 撑字、
    禁用 uppercase。标题 tracking 收紧(-0.01~-0.02em)仅作用于拉丁。

color: # 水墨 + 朱印;明确不用 Tailwind 默认色阶
  bg: "#FAF9F7 — 宣纸暖白(非纯白、非 gray-50)"
  surface: "#FFFFFF — 卡片/面板浮于纸上"
  surface_sunken: "#F4F2EE — 内嵌区(输出区/代码块底)"
  border: "#E7E3DC — 暖褐 1px 分隔线,层级主力(替代阴影)"
  border_strong: "#D6D1C8 — 输入框/强分隔"
  text: "#1A1A18 — 墨(暖近黑,非 #000)"
  text_secondary: "#6B6862 — 暖灰"
  text_muted: "#9A968E — 弱说明/占位"
  accent: "#C8422A — 朱砂(印章)。仅用于:主操作、当前激活态、品牌标记、focus ring"
  accent_hover: "#A8341F"
  accent_wash: "#FBEEEA — 选中行/激活底(极淡朱)"
  secondary: # 靛青 —— 第二角色(信息/数据层),2026-07-26 项目负责人批准入板
    info: "#2B5D8C — 靛青(indigo)。仅用于:链接、信息态提示、图表主数据系列、次级选中态、徽章。
      与朱砂的分工:seal 管『操作』(每屏一枚印),indigo 管『信息』。indigo 永不做主操作按钮、
      永不与 seal 在同一控件上并用;赤陶红×靛蓝互补对只在「印章 vs 数据」的层面成立。"
    info_hover: "#1F4A73"
    info_wash: "#EAF1F7 — 信息条底/选中行/图表浅填充"
  states: # 与 accent 刻意拉开明度/色相,并强制配图标+文案,不靠颜色单独表意
    success: "#2F6B4F — 竹绿(深,非亮绿)"
    warning: "#9A6B1E — 赭黄"
    error: "#8C1D18 — 深绛(明显比 accent 更深更冷;破坏性操作用描边+明确动词,不与主操作同形)"
  contrast_floor: "正文/次要文字 ≥ WCAG AA 4.5:1(text_muted 仅用于 ≥13px 非关键信息);朱砂按钮白字 ≥4.5:1;focus ring 3:1 且永不移除。"

space:
  scale: [4, 8, 12, 16, 24, 32, 48, 64, 96]
  density: >
    calm-dense(冷静的密)——数据行 44~48px、表格行高 44px:密到像仪表;
    区块节奏 48~64px、页面左右留白 ≥24px:疏到像被设计过。
    反面:当前 Playground 无宽度约束、元素散落撑满 1920px —— 明令禁止。

layout:
  container: >
    内容恒有最大宽度并居中容器:营销/公开页 max-w 1120px;控制台内容区 max-w 1080px;
    Playground 双栏 max-w 1200px。左右 gutter 24px(≥lg 32px)。任何页面都不得内容裸贴视口边缘。
  console_shell: "固定左侧导航 240px + 流式内容区(capped 1080px);顶栏 56px,含 ⌘K 搜索。"
  hierarchy: >
    层级只由三样建立:字重/字号、1px 暖边框、surface 与 bg 的明度差。
    禁止用阴影堆层级、禁止用彩色块分区。
  alignment: "左对齐编辑式(参考 Linear);标题/正文左对齐,数字右对齐;禁止 everything-centered。"

shape_elevation:
  radius: "控件/卡片 8px;输入/药丸 6px;头像/标签 999px。不用 12~16px 的圆润blob感。"
  shadow: >
    静态表面一律无阴影(用 1px 暖边框)。仅一个 overlay 阴影 token 给真浮层
    (下拉/对话框/气泡):0 8px 24px rgba(26,26,24,0.10)。禁止 shadow-on-everything。

motion:
  timing: "状态 120ms;进入/退出 180ms"
  easing: "cubic-bezier(0.2, 0, 0, 1)"
  use_where: >
    仅四处:focus ring 出现、hover 时边框加深、浮层进出、流式输出的光标/逐字。
    刻意不用:页面转场、滚动揭示、弹簧回弹、渐变流动、骨架屏闪烁。克制本身就是动效主张。

icons: "线性图标 1.5px 描边(lucide),尺寸 16/20;禁止 emoji 充当产品图标。"

anti_slop_compliance: # 逐条对照禁忌
  - "✅ 非 Inter/Roboto:IBM Plex 家族(双语+等宽+可自托管,理由充分)"
  - "✅ 无紫蓝渐变 hero:宣纸底 + 墨字 + 单枚朱印"
  - "✅ 无三栏圆角卡片墙:模型市场采用 OpenRouter 验证过的分面+密集列表"
  - "✅ 无处处阴影:1px 暖边框建立层级,仅浮层一个阴影 token"
  - "✅ 非 Tailwind 默认色阶:自定义 宣纸/墨/朱砂 色板"
  - "✅ 无 emoji 图标:lucide 线性图标"
  - "✅ 非全局居中:左对齐编辑式版式"
  - "✅ 无无理由玻璃拟态/渐变"

tailwind_mapping: # 落地锚点(实现层 Tailwind + Next.js App Router)
  - "theme.extend.colors: paper/surface/ink/muted/seal(朱砂)/jade/ochre/crimson 语义命名,不暴露 blue-500 式默认色"
  - "theme.extend.fontFamily: sans=[IBM Plex Sans, PingFang SC, Microsoft YaHei, Noto Sans CJK SC, sans-serif]; mono=[IBM Plex Mono, ui-monospace]"
  - "theme.extend.maxWidth: prose-page=1120px / console=1080px / playground=1200px"
  - "Plex Sans/Mono 自托管于 /public/fonts + next/font/local(仅 subset 拉丁+数字;禁走 Google Fonts CDN,大陆不可达);中文不加载 webfont,落到系统栈"
  - "数字统一用 .tabular 工具类(font-mono + font-variant-numeric: tabular-nums)"

i18n_rtl:
  - "逻辑属性优先(ps/pe/ms/me、text-start/end),不用 left/right,以支持 ar 等 RTL"
  - "中文不做 uppercase/letter-spacing;数字与拉丁保持 Plex Mono/Sans,不随中文字体走"
  - "行高按 CJK +0.1;标签避免拉丁大写风格直译到中文"

key_page_direction: # 方向性改造建议(design-ui 逐页落地)
  playground: >
    收进 max-w 1200 双栏:左 480px(模型选择器/参数/System/User 输入)、右 流式输出 + 代码导出。
    模型选择器走 ⌘K 命令式;输出区 surface_sunken + Plex Mono;唯一朱砂落在「发送」。
    彻底消灭现在的撑满整屏+散落布局。
  models: >
    改为 左侧分面筛选(厂商/模态/上下文/能力/价格) + 中间密集列表行(替换现有卡片网格)。
    每行:厂商图标 + 模型名(Plex Sans 600)+ 2 行描述 + 指标行(上下文 / 输入价 / 输出价,
    全 Plex Mono tabular)。顶部搜索 + 排序 + 模态标签带数量。
    ⚠️ 依赖:后端 /public/models 目前不返回价格,需补(价格是网关产品的核心决策信息)。
  dashboard: >
    指标卡改「仪表」:metric-lg Plex Mono 大数 + label 小标签 + 迷你趋势线;
    空/错状态用一行安静说明 + 文字链重试,不要满宽红底 banner。
  auth: >
    收窄至 400px 卡片、左对齐品牌印记;朱砂只落在「登录」主按钮;
    OAuth 按钮降为次级(描边),不与主操作抢。

provenance: { source: design-system, added: "2026-07-11", approved_by: "项目负责人 (2026-07-11, 选项A:认可方向,朱砂主色保留)" }
```
