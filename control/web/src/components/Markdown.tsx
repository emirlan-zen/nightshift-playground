import { useMemo } from "react";
import { renderMarkdown } from "@/lib/format";
import { cn } from "@/lib/utils";

export function Markdown({ source, className }: { source: string; className?: string }) {
  const html = useMemo(() => renderMarkdown(source), [source]);
  return <div className={cn("prose-ns", className)} dangerouslySetInnerHTML={{ __html: html }} />;
}
