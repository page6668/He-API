import type { Config } from 'tailwindcss';
import animate from 'tailwindcss-animate';

/**
 * He-API 设计系统 token —— 权威定义见 knowledge/taste/design-system.md
 * (POV「宣纸·墨·朱砂印」,项目负责人 2026-07-11 批准)。
 *
 * 两条铁律:
 *   1. 数字一律 font-mono(IBM Plex Mono)+ tabular-nums(用 .tabular 工具类);正文永不等宽。
 *   2. seal(朱砂)每屏只落一处 —— 主操作或当前激活态。禁止铺底/渐变/装饰。
 *
 * 层级只由 字重字号 / 1px 暖边框 / surface 明度差 建立。静态表面禁止阴影(仅 overlay 一个)。
 */
const config: Config = {
  darkMode: ['class'],
  content: ['./app/**/*.{ts,tsx}', './components/**/*.{ts,tsx}', './lib/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        // 水墨底色系(刻意不用 Tailwind 默认色阶)
        paper: '#FAF9F7', // 宣纸暖白(页面底)
        surface: {
          DEFAULT: '#FFFFFF', // 卡片/面板
          sunken: '#F4F2EE', // 内嵌区:输出区/代码块
        },
        ink: {
          DEFAULT: '#1A1A18', // 墨(正文)
          secondary: '#6B6862',
          muted: '#9A968E',
        },
        line: {
          DEFAULT: '#E7E3DC', // 暖褐 1px 分隔线 —— 层级主力
          strong: '#D6D1C8', // 输入框/强分隔
        },
        // 朱砂印 —— 每屏只落一处
        seal: {
          DEFAULT: '#C8422A',
          hover: '#A8341F',
          wash: '#FBEEEA',
        },
        // 语义色:与 seal 刻意拉开,且必须配图标+文案,不靠颜色单独表意
        jade: '#2F6B4F', // success
        ochre: '#9A6B1E', // warning
        crimson: '#8C1D18', // error / destructive(比 seal 更深更冷)
        // 靛青 —— 第二角色(信息/数据),2026-07-26 批准入板。
        // 分工:seal 管「操作」(主按钮/激活态,每屏一处),indigo 管「信息」
        // (链接/信息态/图表主系列/次级选中/徽章)。indigo 不做主操作按钮。
        // 注意:此定义整体替换 Tailwind 默认 indigo 色阶(50~950 不复存在)。
        indigo: {
          DEFAULT: '#2B5D8C', // 靛青(paper 上 ≈6.5:1,AA 正文可用)
          hover: '#1F4A73',
          wash: '#EAF1F7', // 信息条底/选中行/图表浅填充
        },
        // 既有 shadcn 风格别名(旧组件仍在引用,统一指向新色板)
        border: '#E7E3DC',
        background: '#FAF9F7',
        foreground: '#1A1A18',
        primary: { DEFAULT: '#C8422A', foreground: '#FFFFFF' },
        muted: { DEFAULT: '#F4F2EE', foreground: '#6B6862' },
      },
      fontFamily: {
        // Plex 由 next/font/google 在构建时下载并自托管(运行时不碰 CDN,大陆可用);
        // 中文落系统栈 —— 刻意不加载 CJK webfont(全量 10MB+ 会毁首屏)。
        sans: [
          'var(--font-plex-sans)',
          'PingFang SC',
          'Microsoft YaHei',
          'Noto Sans CJK SC',
          'sans-serif',
        ],
        mono: ['var(--font-plex-mono)', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
      fontSize: {
        // 见 design-system.md typography.scale
        label: ['12px', { lineHeight: '1.4', fontWeight: '500', letterSpacing: '0.02em' }],
        small: ['13px', { lineHeight: '1.5' }],
        body: ['15px', { lineHeight: '1.6' }],
        h3: ['17px', { lineHeight: '1.4', fontWeight: '600' }],
        h2: ['22px', { lineHeight: '1.3', fontWeight: '600' }],
        h1: ['30px', { lineHeight: '1.25', fontWeight: '600', letterSpacing: '-0.01em' }],
        display: ['44px', { lineHeight: '1.1', fontWeight: '600', letterSpacing: '-0.02em' }],
        metric: ['15px', { lineHeight: '1.4', fontWeight: '500' }],
        'metric-lg': ['28px', { lineHeight: '1.1', fontWeight: '600' }],
      },
      maxWidth: {
        // 内容恒有最大宽度 —— 治「撑满整屏」
        'prose-page': '1120px',
        console: '1080px',
        playground: '1200px',
      },
      borderRadius: {
        lg: '8px', // 卡片/控件
        md: '6px', // 输入/药丸
        sm: '4px',
      },
      boxShadow: {
        // 唯一允许的阴影:真浮层(下拉/对话框)。静态表面一律用 1px 暖边框。
        overlay: '0 8px 24px rgba(26, 26, 24, 0.10)',
      },
      transitionTimingFunction: {
        he: 'cubic-bezier(0.2, 0, 0, 1)',
      },
      transitionDuration: {
        state: '120ms',
        enter: '180ms',
      },
    },
  },
  plugins: [animate],
};

export default config;
