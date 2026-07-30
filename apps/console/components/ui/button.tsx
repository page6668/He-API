import * as React from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

const buttonVariants = cva(
  'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg text-sm font-medium transition-all duration-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-orange-400 focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        default:
          'bg-gradient-to-r from-orange-500 to-orange-600 text-white shadow-md hover:from-orange-400 hover:to-orange-500 hover:shadow-glow-orange hover:scale-[1.02] active:scale-[0.98]',
        secondary:
          'bg-gradient-to-r from-blue-500 to-blue-600 text-white shadow-md hover:from-blue-400 hover:to-blue-500 hover:shadow-glow-blue hover:scale-[1.02] active:scale-[0.98]',
        destructive:
          'bg-red-500 text-white shadow-sm hover:bg-red-600 hover:shadow-md active:scale-[0.98]',
        outline:
          'border-2 border-slate-200 dark:border-slate-700 bg-transparent hover:bg-slate-100 dark:hover:bg-slate-800 text-slate-700 dark:text-slate-300',
        outlineOrange:
          'border-2 border-orange-500 text-orange-600 dark:text-orange-400 bg-transparent hover:bg-orange-50 dark:hover:bg-orange-950/30 hover:border-orange-400',
        outlineBlue:
          'border-2 border-blue-500 text-blue-600 dark:text-blue-400 bg-transparent hover:bg-blue-50 dark:hover:bg-blue-950/30 hover:border-blue-400',
        ghost:
          'text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 hover:text-slate-900 dark:hover:text-white',
        link: 'text-orange-600 dark:text-orange-400 underline-offset-4 hover:underline',
        success:
          'bg-gradient-to-r from-green-500 to-green-600 text-white shadow-md hover:from-green-400 hover:to-green-500 hover:shadow-md active:scale-[0.98]',
      },
      size: {
        default: 'h-10 px-4 py-2',
        sm: 'h-8 rounded-md px-3 text-xs',
        lg: 'h-12 rounded-lg px-8 text-base',
        xl: 'h-14 rounded-lg px-10 text-lg',
        icon: 'h-10 w-10',
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
      <Comp
        className={twMerge(clsx(buttonVariants({ variant, size, className })))}
        ref={ref}
        {...props}
      />
    );
  }
);
Button.displayName = 'Button';

export { Button, buttonVariants };
