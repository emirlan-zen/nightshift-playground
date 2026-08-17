import type { HTMLAttributes } from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1.5 font-mono text-[9.5px] font-medium uppercase tracking-wider px-2 py-[3px] border whitespace-nowrap",
  {
    variants: {
      tone: {
        muted: "border-line2 text-mut",
        ok: "border-ok text-ok",
        accent: "border-accent text-accent",
        sched: "border-sched/60 text-sched",
        review: "border-review text-review",
        danger: "border-danger/70 text-danger",
        done: "border-line2 text-dim",
      },
    },
    defaultVariants: { tone: "muted" },
  },
);

export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {
  dot?: boolean;
}

export function Badge({ className, tone, dot, children, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ tone }), className)} {...props}>
      {dot && <span className="size-1.5 rounded-full bg-current" />}
      {children}
    </span>
  );
}
