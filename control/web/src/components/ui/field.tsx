import * as React from "react";
import { cn } from "@/lib/utils";

const base =
  "w-full bg-bg text-ink border border-line2 px-3 py-2.5 font-mono text-[13px] placeholder:text-dim focus:outline-none focus:border-accent";

export const Textarea = React.forwardRef<
  HTMLTextAreaElement,
  React.TextareaHTMLAttributes<HTMLTextAreaElement>
>(({ className, ...props }, ref) => (
  <textarea
    ref={ref}
    className={cn(base, "min-h-[74px] resize-y leading-relaxed", className)}
    {...props}
  />
));
Textarea.displayName = "Textarea";

export const Input = React.forwardRef<
  HTMLInputElement,
  React.InputHTMLAttributes<HTMLInputElement>
>(({ className, ...props }, ref) => <input ref={ref} className={cn(base, className)} {...props} />);
Input.displayName = "Input";
