/**
 * He-API UI kit —— 全站共享基元。权威设计定义见 knowledge/taste/design-system.md
 * (POV「宣纸·墨·朱砂印」,2026-07-11 批准);参考实现见 Playground(specs/playground-ui.md)。
 *
 * 存在的理由:Playground 落地时把 fieldCls/labelCls/panelCls 写成了页面局部常量,
 * 全站铺开时必须提为共享,否则各页各写一套 → 设计一致性必然崩。任何页面都应从这里取,
 * 不要重新发明。
 *
 * 两条铁律(实现层约束):
 *   1. 数字用 `tabular` 类(等宽 + 制表对齐)—— 上下文/单价/延迟/Token/百分比。正文永不用。
 *   2. seal(朱砂)每屏只落一处 —— 用 <Button variant="primary"> 表达那唯一的主操作;
 *      其余操作用 secondary/ghost。激活态若已有主操作占用朱砂,请改用墨色。
 */
import type { ReactNode } from 'react';

/* ---------- 表单原子(样式常量,供原生 input/select/textarea 直接套用) ---------- */

/** 输入类控件:6px 圆角、暖边框、focus 落朱砂环。数值输入请再叠加 `tabular`。 */
export const fieldCls =
  'mt-1.5 w-full rounded-md border border-line-strong bg-surface px-3 py-2 text-small text-ink outline-none transition-colors duration-state ease-he placeholder:text-ink-muted focus:border-seal focus:ring-2 focus:ring-seal/15 disabled:cursor-not-allowed disabled:opacity-50';

/** 字段标签:12/500。刻意不 uppercase(中文禁用大写与字距)。 */
export const labelCls = 'block text-label text-ink-secondary';

/** 面板/卡片:1px 暖边框 + 白面,零阴影(层级靠边框与明度差)。 */
export const panelCls = 'rounded-lg border border-line bg-surface';

/**
 * 正文链接:靛青 + 下划线(indigo 管「信息」—— 链接/信息态/徽章;seal 管「操作」)。
 * 注意:导航项**不用**此类 —— 下划线策略是「正文内链接有下划线,导航项无」。
 */
export const linkCls =
  'rounded-sm text-indigo underline underline-offset-2 transition-colors duration-state ease-he hover:text-indigo-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30';

/* ---------- 组件 ---------- */

interface PageShellProps {
  /** 页面标题(text-h1,左对齐,不居中)。 */
  title: string;
  /** 标题元素 id —— 供外层 landmark 用 aria-labelledby 指向它(保留既有 a11y 接线)。 */
  titleId?: string;
  /** 副标题/一句话说明。 */
  subtitle?: string;
  /** 标题右侧的操作区(如「New key」)。 */
  actions?: ReactNode;
  /** 容器宽度语义:console 内容 1080 / 公开页 1120 / 双栏工作台 1200。 */
  width?: 'console' | 'prose-page' | 'playground';
  children: ReactNode;
}

/**
 * 页面外壳:**内容恒有最大宽度并居中** —— 这是治「撑满整屏」的唯一入口。
 * 所有页面都必须包一层 PageShell,禁止内容裸贴视口边缘。
 */
export function PageShell({
  title,
  titleId,
  subtitle,
  actions,
  width = 'console',
  children,
}: PageShellProps) {
  const maxW =
    width === 'playground'
      ? 'max-w-playground'
      : width === 'prose-page'
        ? 'max-w-prose-page'
        : 'max-w-console';
  return (
    <section className={`mx-auto ${maxW} px-6 py-12 lg:px-8`}>
      <header className="mb-6 flex items-start justify-between gap-6">
        <div>
          <h1 id={titleId} className="text-h1">
            {title}
          </h1>
          {subtitle && <p className="mt-1 text-small text-ink-secondary">{subtitle}</p>}
        </div>
        {actions && <div className="shrink-0">{actions}</div>}
      </header>
      {children}
    </section>
  );
}

/** 内容面板。`inset` 用于内嵌读数区(输出/代码/表格底)。 */
export function Panel({
  children,
  className = '',
  inset = false,
  padded = true,
}: {
  children: ReactNode;
  className?: string;
  inset?: boolean;
  padded?: boolean;
}) {
  const bg = inset ? 'bg-surface-sunken' : 'bg-surface';
  return (
    <div className={`rounded-lg border border-line ${bg} ${padded ? 'p-6' : ''} ${className}`}>
      {children}
    </div>
  );
}

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger';

const buttonVariants: Record<ButtonVariant, string> = {
  // 唯一的朱砂 —— 每屏只允许一个 primary。
  primary: 'bg-seal text-white hover:bg-seal-hover disabled:opacity-40',
  secondary:
    'border border-line-strong bg-surface text-ink hover:border-ink-muted hover:bg-surface-sunken disabled:opacity-50',
  ghost: 'text-ink-secondary hover:bg-surface-sunken hover:text-ink disabled:opacity-50',
  // 破坏性操作:深绛 + 描边 + 明确动词(刻意不与 primary 同形,避免与朱砂混淆)。
  danger:
    'border border-crimson/40 bg-transparent text-crimson hover:bg-crimson/5 disabled:opacity-50',
};

export function Button({
  variant = 'secondary',
  className = '',
  type = 'button',
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  return (
    <button
      type={type}
      // focus ring 一律朱砂(design-system.md contrast_floor:focus ring 永不移除)。
      className={`rounded-md px-4 py-2 text-small font-medium transition-colors duration-state ease-he focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30 focus-visible:ring-offset-2 disabled:cursor-not-allowed ${buttonVariants[variant]} ${className}`}
      {...props}
    />
  );
}

/** 仪表读数:大号等宽数字 + 小标签(Dashboard 指标卡)。 */
export function Metric({
  label,
  value,
  hint,
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
}) {
  return (
    <div>
      <dt className="text-label text-ink-muted">{label}</dt>
      <dd className="tabular mt-1 text-metric-lg text-ink">{value}</dd>
      {hint && <p className="mt-0.5 text-label text-ink-muted">{hint}</p>}
    </div>
  );
}

/** 空状态:引导而非道歉(brand.md tone_rules)。 */
export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-dashed border-line-strong px-6 py-12 text-center">
      <p className="text-h3 text-ink">{title}</p>
      {description && <p className="mx-auto mt-1.5 max-w-md text-small text-ink-secondary">{description}</p>}
      {action && <div className="mt-4 flex justify-center">{action}</div>}
    </div>
  );
}

/** 行内提示条:安静、不喧宾夺主(禁止满宽红底 banner)。info 走靛青(indigo 管「信息」)。 */
export function Notice({
  tone = 'neutral',
  children,
  role,
}: {
  tone?: 'neutral' | 'info' | 'warning' | 'error';
  children: ReactNode;
  role?: 'status' | 'alert';
}) {
  const tones = {
    neutral: 'border-line bg-surface text-ink-secondary',
    info: 'border-indigo/30 bg-indigo-wash text-indigo',
    warning: 'border-ochre/30 bg-ochre/5 text-ochre',
    error: 'border-crimson/30 bg-crimson/5 text-crimson',
  } as const;
  return (
    <div role={role} className={`rounded-lg border px-4 py-3 text-small ${tones[tone]}`}>
      {children}
    </div>
  );
}

/**
 * 徽章:小型状态/信息标记(不可点、不承载操作)。
 * 分工:info/neutral 走靛青/灰阶(信息);success/warning/error 走 jade/ochre/crimson
 * (语义结果,须配文案表意,不靠颜色单独区分)。徽章永不用 seal —— 朱砂只管操作。
 */
export function Badge({
  tone = 'neutral',
  className = '',
  children,
}: {
  tone?: 'neutral' | 'info' | 'success' | 'warning' | 'error';
  className?: string;
  children: ReactNode;
}) {
  const tones = {
    neutral: 'border-line bg-surface-sunken text-ink-secondary',
    info: 'border-indigo/25 bg-indigo-wash text-indigo',
    success: 'border-jade/25 bg-jade/5 text-jade',
    warning: 'border-ochre/25 bg-ochre/5 text-ochre',
    error: 'border-crimson/25 bg-crimson/5 text-crimson',
  } as const;
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-label ${tones[tone]} ${className}`}
    >
      {children}
    </span>
  );
}
