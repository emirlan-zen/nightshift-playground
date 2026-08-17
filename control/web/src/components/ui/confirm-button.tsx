import { useEffect, useRef, useState } from "react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { cn } from "@/lib/utils";

interface ConfirmButtonProps extends Omit<ButtonProps, "onClick" | "children"> {
  onConfirm: () => void;
  /** resting label, e.g. "Stop" */
  label: string;
  /** label while the action is in flight */
  pendingLabel?: string;
  pending?: boolean;
}

// Two-step destructive control: first tap arms ("Sure?"), a second tap within
// 3s fires. No blocking dialog — thumb-safe on a phone, on-brand inline. Auto
// disarms so a stray first tap can't leave a live grenade on the card.
export function ConfirmButton({
  onConfirm,
  label,
  pendingLabel = "…",
  pending,
  disabled,
  className,
  size = "sm",
  ...rest
}: ConfirmButtonProps) {
  const [armed, setArmed] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  const click = () => {
    if (pending) return;
    if (armed) {
      clearTimeout(timer.current);
      setArmed(false);
      onConfirm();
    } else {
      setArmed(true);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setArmed(false), 3000);
    }
  };

  return (
    <Button
      {...rest}
      size={size}
      variant="danger"
      disabled={disabled || pending}
      onClick={click}
      className={cn(armed && "bg-danger text-ink-on-accent hover:bg-danger", className)}
    >
      {pending ? pendingLabel : armed ? "Sure?" : label}
    </Button>
  );
}
