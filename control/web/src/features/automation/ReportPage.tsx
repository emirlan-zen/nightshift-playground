import { useNavigate, useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { api } from "@/lib/api";
import { qk } from "@/lib/queries";
import { fmtRunId } from "@/lib/format";
import { Badge } from "@/components/ui/badge";
import { Markdown } from "@/components/Markdown";
import { EmptyText, ErrorText } from "@/components/SectionHeader";
import { HiddenAgentNotice } from "@/components/HiddenAgentNotice";
import { useAgentHidden } from "@/lib/visibility";

// Morning reports are long markdown; a phone modal double-scrolls and wastes
// width (2.3). Reached at /night/:agent/:id (route already existed), now a full
// page: back link + banner + rendered report.
const toneChip: Record<string, "ok" | "sched" | "muted" | "danger"> = {
  shipped: "ok",
  partial: "sched",
  quiet: "muted",
  stall: "danger",
};

export function ReportPage() {
  const { agent, id } = useParams();
  const navigate = useNavigate();
  const hidden = useAgentHidden(agent);
  const report = useQuery({
    queryKey: ["report", agent, id],
    queryFn: () => api.report(agent!, id!),
    enabled: !!agent && !!id && !hidden,
  });
  const reports = useQuery({
    queryKey: qk.reports(agent!),
    queryFn: () => api.reports(agent!),
    enabled: !!agent && !hidden,
  });
  if (!agent || !id) return null;
  if (hidden) return <HiddenAgentNotice />;
  const meta = reports.data?.find((r) => r.id === id);
  const h = fmtRunId(id);

  return (
    <section>
      <button
        onClick={() => navigate(-1)}
        className="mb-4 inline-flex items-center gap-1 font-mono text-[11px] uppercase tracking-wider text-mut transition-colors hover:text-ink"
      >
        <ChevronLeft className="size-3.5" /> Back
      </button>

      <div className="mb-1 flex flex-wrap items-center gap-2.5">
        {meta?.tone && <Badge tone={toneChip[meta.tone] ?? "muted"}>{meta.tone}</Badge>}
        <h1 className="font-head text-[22px] font-bold leading-tight tracking-tight">
          {meta?.headline || h.label}
        </h1>
      </div>
      <div className="mb-4 break-all font-mono text-[10.5px] tracking-wide text-dim">
        {agent} · {[h.label, meta?.wave, meta?.stats].filter(Boolean).join(" · ")} · {id}
      </div>

      {meta?.banner && (
        <img
          src={api.reportBanner(agent, id)}
          alt=""
          className="mb-4 block w-full border border-line"
        />
      )}

      {report.isLoading && <EmptyText>Loading report…</EmptyText>}
      {report.error instanceof Error && <ErrorText>{report.error.message}</ErrorText>}
      {report.data && (
        <div className="prose-ns max-w-[72ch]">
          <Markdown source={report.data} />
        </div>
      )}
    </section>
  );
}
