import * as React from "react";
import * as SwitchPrimitive from "@radix-ui/react-switch";
import { cn } from "@/lib/utils";

// Sharp, hard-edged toggle: rectangular track + square knob (no rounding), to
// match the rest of the surface. Green when on.
export const Switch = React.forwardRef<
  React.ElementRef<typeof SwitchPrimitive.Root>,
  React.ComponentPropsWithoutRef<typeof SwitchPrimitive.Root>
>(({ className, ...props }, ref) => (
  <SwitchPrimitive.Root
    ref={ref}
    className={cn(
      "peer relative inline-flex h-5 w-10 shrink-0 items-center border transition-colors",
      "border-line2 bg-bg data-[state=checked]:border-ok data-[state=checked]:bg-ok/20",
      "disabled:opacity-50 disabled:pointer-events-none focus-visible:outline-none",
      className,
    )}
    {...props}
  >
    <SwitchPrimitive.Thumb
      className={cn(
        "pointer-events-none block size-3.5 translate-x-[3px] bg-dim transition-transform",
        "data-[state=checked]:translate-x-[21px] data-[state=checked]:bg-ok",
      )}
    />
  </SwitchPrimitive.Root>
));
Switch.displayName = "Switch";
