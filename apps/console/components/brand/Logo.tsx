/**
 * He-API 品牌标记。规范见 knowledge/taste/brand.md(logo_usage / do_not)。
 *
 * 印记 = 朱砂方印(白文/阴刻):实心印底 + 负形刻出「多路汇一」——三路模型输入
 * 汇成一个接口,即产品本身。印记是品牌唯一允许的朱砂大色块;页面其余部分朱砂
 * 只落一处(主操作或激活态)。
 *
 * 不得:拉伸变形、加渐变、加阴影、改朱砂色、旋转、emoji 替代(brand.md do_not)。
 */

interface LogoMarkProps {
  /** 印记边长(px)。最小 20。 */
  size?: number;
  className?: string;
}

export function LogoMark({ size = 24, className }: LogoMarkProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
      focusable="false"
      className={className}
    >
      {/* 刀刻直笔 + 圆角 3:方正如印。勿改大圆角(会变成 app 图标)。 */}
      <rect width="32" height="32" rx="3" fill="#C8422A" />
      <g stroke="#FAF9F7" strokeWidth="2.6" strokeLinecap="square" strokeLinejoin="miter" fill="none">
        <path d="M8 8.5 L17.5 16" />
        <path d="M8 16 H17.5" />
        <path d="M8 23.5 L17.5 16" />
        <path d="M17.5 16 H24" />
      </g>
    </svg>
  );
}

interface LogoProps {
  /** 印记边长;字标随之缩放。 */
  size?: number;
  /** 只要印记,不要字标(窄容器/头像位)。 */
  markOnly?: boolean;
  className?: string;
}

/**
 * 横向锁定组合:印记 + 「He-API」字标。留白 ≥ 印章边长的 0.5 倍由外部容器保证。
 * 字标用 Plex Sans 600(app 已自托管),中文语境同样写作 He-API(brand.md naming)。
 */
export function Logo({ size = 24, markOnly = false, className }: LogoProps) {
  return (
    <span className={`inline-flex items-center gap-2 ${className ?? ''}`}>
      <LogoMark size={size} />
      {!markOnly && (
        <span
          className="font-semibold tracking-[-0.01em] text-ink"
          style={{ fontSize: Math.round(size * 0.72) }}
        >
          He-API
        </span>
      )}
      <span className="sr-only">He-API</span>
    </span>
  );
}
