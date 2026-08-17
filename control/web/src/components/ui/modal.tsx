import * as React from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

interface ModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  children: React.ReactNode;
}

// Bottom-sheet on phones, centered dialog on desktop. Same component both ways
// so ticket / report / prompt detail share one presentation.
export function Modal({ open, onOpenChange, title, subtitle, children }: ModalProps) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay
          className={cn(
            "fixed inset-0 z-50 bg-black/65 backdrop-blur-[2px]",
            "data-[state=open]:animate-[fade_.18s_ease]",
          )}
        />
        <Dialog.Content
          className={cn(
            "fixed z-50 flex flex-col bg-surface border border-line2 will-change-transform",
            "inset-x-0 bottom-0 max-h-[88dvh] pb-[env(safe-area-inset-bottom)]",
            "sm:inset-auto sm:left-1/2 sm:top-1/2 sm:w-[min(680px,92vw)] sm:max-h-[86vh] sm:-translate-x-1/2 sm:-translate-y-1/2",
            // fill-mode `both` is load-bearing: without it the browser paints one
            // frame at the element's pre-animation position (top-left flash)
            // before the keyframe's `from` applies. `both` uses the `from` frame
            // (already centered / translated) from the very first paint.
            "data-[state=open]:animate-[sheet_.2s_cubic-bezier(.16,1,.3,1)_both] sm:data-[state=open]:animate-[pop_.18s_ease_both]",
            "focus:outline-none",
          )}
        >
          <div className="sticky top-0 flex items-start gap-3 border-b border-line bg-surface px-[18px] py-4">
            <div className="min-w-0 flex-1">
              <Dialog.Title className="font-head text-base font-bold leading-snug tracking-tight break-words">
                {title}
              </Dialog.Title>
              {subtitle && (
                <Dialog.Description className="mt-1.5 font-mono text-[10.5px] text-dim break-words tracking-wide">
                  {subtitle}
                </Dialog.Description>
              )}
            </div>
            <Dialog.Close
              aria-label="Close"
              className="grid size-10 shrink-0 place-items-center border border-line2 text-ink transition-colors hover:border-accent hover:text-accent"
            >
              <X className="size-4" />
            </Dialog.Close>
          </div>
          <div className="overflow-auto px-[18px] py-[18px]">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
