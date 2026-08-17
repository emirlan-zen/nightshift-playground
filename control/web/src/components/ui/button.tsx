import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap font-mono uppercase tracking-wider font-bold select-none transition-colors touch-manipulation disabled:opacity-50 disabled:pointer-events-none focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent",
  {
    variants: {
      variant: {
        accent: "bg-accent text-ink-on-accent hover:bg-accent-hi",
        ghost: "bg-transparent text-ink border border-line2 hover:border-ink",
        danger: "bg-transparent text-danger border border-danger hover:bg-danger/15",
        subtle: "bg-transparent text-ink border border-line2 hover:border-ink",
      },
      // Heights meet touch-target guidance (workspace/DESIGN.md + a11y): primary
      // actions (md/block) land at 44px, compact inline actions (sm) at 40px.
      size: {
        block: "w-full min-h-11 px-4 py-3 text-[11px]",
        sm: "min-h-10 px-3 py-2 text-[10px]",
        md: "min-h-11 px-4 py-2.5 text-[11px]",
      },
    },
    defaultVariants: { variant: "ghost", size: "md" },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp ref={ref} className={cn(buttonVariants({ variant, size }), className)} {...props} />
    );
  },
);
Button.displayName = "Button";

export { buttonVariants };
