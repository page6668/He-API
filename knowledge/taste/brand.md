# taste/brand

Brand voice and identity `design-ui` and copy decisions read. Authored by the
`design-system` skill (and refined by the metabolism loop). Structured, with
provenance.

```yaml
voice: >
  工程师对工程师说话:平实、确定、可核验。像仪表读数,不像销售话术。
  说"32K 上下文 / ¥0.15 每百万 Token",不说"超强性能,极致体验"。
  英文同调:plain, precise, no hype —— 陈述事实与数字,不用形容词堆砌。

tone_rules:
  - "动词优先于名词:『创建密钥』而非『密钥创建功能』;Create a key, not Key creation."
  - "禁用感叹号(错误与营销文案一律不用)。"
  - "禁用夸张形容词:极致/强大/领先/革命性/blazing/supercharged/seamless。"
  - "数字代替形容词:能用数值说明的,绝不用形容词(『131K 上下文』而非『超长上下文』)。"
  - "错误文案三段式:发生了什么 + 为什么(如可知)+ 下一步怎么办。不指责用户,不用『非法/失败』这类冷硬词,用『无效/未成功』。"
  - "空状态是引导不是道歉:『创建第一个 API 密钥即可开始调用』,不写『暂无数据』了事。"
  - "中文用全角标点(,。?…),中英混排时数字/拉丁两侧留一个空格。"
  - "第二人称『你』(不用『您』——工程师同侪口吻,与平实调性一致);英文用 you。"
  - "不承诺未上线能力;占位/未接通的功能明确标注,不含糊。"

naming:
  product: "He-API(始终大小写如此,不写 HeAPI / he-api / HE-API;中文语境亦用 He-API,不音译)"
  features: "用行业通用名,不造词:模型市场 Models / 在线体验 Playground / 用量 Usage / API 密钥 API Keys / 请求日志 Logs / 基准测试 Benchmark。"
  models: "模型 id 原样呈现且用等宽字(qwen-max、deepseek-v3);厂商用官方中文名+英文(通义千问 Qwen / 深度求索 DeepSeek / 豆包 Doubao / 文心 ERNIE / 智谱 GLM / 月之暗面 Kimi)。"
  routing_models: "he-router-* 是 He-API 自有的路由模型,呈现时标注为『智能路由』并说明依据(成本/质量/延迟),不伪装成第三方模型。"

logo_usage: >
  品牌标记 = 一枚朱砂印(方形印章意象)+ 「He-API」字标(Plex Sans 600)。
  留白 ≥ 印章边长的 0.5 倍;最小高度 20px(印章)/ 16px(字标)。
  不得:拉伸变形、加渐变、加阴影、改朱砂色、旋转、放在花哨背景上、用 emoji 替代。
  深色背景上用宣纸白字标 + 原色朱印。

do_not: # 品牌红线
  - "不用紫色/紫蓝渐变——那是 OpenRouter 的地盘,抄它等于承认自己是仿品。"
  - "朱砂红不得大面积铺底、不得做渐变、不得当装饰;它是印章,每屏只落一处。"
  - "不用 AI 味插画(抽象神经网络、发光大脑、粒子星云)。要展示就展示产品本体与真实数字。"
  - "不用 emoji 当产品图标或标题装饰。"
  - "不做『企业级/赋能/生态』这类空话;每句话必须能被一个数字或一个动作验证。"
  - "不在中文界面留未翻译英文(登录页曾整段英文——已修,不得复发)。"
  - "不用 Inter/Roboto——字体是品牌的一部分,统一 IBM Plex 家族。"

provenance: { source: design-system, added: "2026-07-11", approved_by: "项目负责人 (2026-07-11, 选项A)" }
```

<!--
Metabolism: when a human corrects brand/voice at an accept gate, append or
supersede an entry here (with date + approver). Supersede, don't delete.
-->
