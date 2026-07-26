import * as React from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '@/lib/utils';

const buttonVariants = cva(
  // focus ring 一律朱砂(primary 别名即 seal);动效走 duration-state + ease-he。
  'inline-flex items-center justify-center whitespace-nowrap rounded-md text-small font-medium ring-offset-background transition-colors duration-state ease-he focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50',
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground hover:bg-seal-hover',
        outline: 'border border-line-strong bg-surface hover:border-ink-muted hover:bg-surface-sunken',
        ghost: 'text-ink-secondary hover:bg-surface-sunken hover:text-ink',
        // Story 5.5 BR-R-4 — destructive (revoke) action。深绛描边(design-system.md
        // states.error:破坏性操作用描边+明确动词,不与主操作同形,避免与朱砂混淆)。
        destructive: 'border border-crimson/40 bg-transparent text-crimson hover:bg-crimson/5',
      },
      size: {
        default: 'h-9 px-4 py-2',
        sm: 'h-8 rounded-md px-3 text-xs',
        icon: 'h-9 w-9',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  }
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : 'button';
    return (
      <Comp className={cn(buttonVariants({ variant, size, className }))} ref={ref} {...props} />
    );
  }
);
Button.displayName = 'Button';

export { Button, buttonVariants };
