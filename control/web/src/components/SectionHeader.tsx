import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function SectionHeader({
  title,
  meta,
  action,
  className,
}: {
  title: ReactNode;
  meta?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex items-center justify-between gap-3 border-b border-line pb-[9px]",
        className,
      )}
    >
      <div className="flex min-w-0 items-baseline gap-2.5">
        <h2 className="font-head text-[15px] font-bold tracking-tight">{title}</h2>
        {meta && (
          <span className="font-mono text-[10.5px] uppercase tracking-wider text-mut">{meta}</span>
        )}
      </div>
      {action}
    </div>
  );
}

export function ErrorText({ children }: { children?: ReactNode }) {
  if (!children) return null;
  return <div className="mt-3 break-words font-mono text-[12.5px] text-danger">{children}</div>;
}

export function EmptyText({ children }: { children: ReactNode }) {
  return <div className="py-4 font-mono text-[12.5px] text-dim">{children}</div>;
}
