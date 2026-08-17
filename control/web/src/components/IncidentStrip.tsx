import { useLocation, useNavigate } from "react-router-dom";
import { ArrowRight, ShieldAlert } from "lucide-react";
import { useFlows, useHealth, useObs, useProposals, useTickets } from "@/lib/queries";
import { cn } from "@/lib/utils";

export function IncidentStrip() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { data: health } = useHealth();
  const { data: obs } = useObs();
  const { data: flows = [] } = useFlows();
  const { data: tickets = [] } = useTickets();
  const { data: proposals = [] } = useProposals();
  // The strip is a pointer to the Inbox; on the Inbox it is pure noise.
  if (pathname.startsWith("/inbox") || !health) return null;

  const authDown = health.auth.checkedAt > 0 && !health.auth.ok;
  // Codex auth (ADR-0018) is a last-known snapshot, not a live ticker — a dead
  // reading still means the next codex wave (review) will skip, so it alerts.
  const codexDown = !!health.codex && health.codex.checkedAt > 0 && !health.codex.ok;
  const forge = (health.forge ?? []).filter((f) => f.checkedAt > 0 && !f.ok);
  const skipped = (health.recentFailures?.length ?? 0) + (health.depSkips?.length ?? 0);
  const alerts = obs?.alerts.length ?? 0;
  const decisions =
    flows.filter((flow) => flow.status === "blocked" || flow.status === "deadline").length +
    tickets.flatMap((group) => group.tickets ?? []).filter((ticket) => ticket.status === "review")
      .length +
    proposals.length;
  const total =
    decisions + (authDown ? 1 : 0) + (codexDown ? 1 : 0) + forge.length + skipped + alerts;
  if (!total) return null;

  const critical = authDown || skipped > 0;
  const headline = authDown
    ? "Claude authentication is blocking unattended work"
    : skipped
      ? `${skipped} unattended stage${skipped === 1 ? "" : "s"} skipped`
      : codexDown
        ? "Codex authentication is blocking the review executor"
        : forge.length
          ? `${forge.length} forge credential${forge.length === 1 ? "" : "s"} need attention`
          : alerts
            ? `${alerts} observability alert${alerts === 1 ? "" : "s"}`
            : `${decisions} decision${decisions === 1 ? "" : "s"} waiting`;

  return (
    <button
      onClick={() => navigate("/inbox")}
      className={cn(
        "mb-4 flex min-h-11 w-full items-center gap-3 border px-3.5 py-2.5 text-left transition-colors hover:bg-raise",
        critical ? "border-danger/55 bg-danger/7" : "border-review/50 bg-review/5",
      )}
    >
      <ShieldAlert className={cn("size-4 shrink-0", critical ? "text-danger" : "text-review")} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[12.5px] font-semibold">{headline}</div>
        <div className="font-mono text-[9.5px] uppercase tracking-wider text-dim">
          {total} item{total === 1 ? "" : "s"} in Inbox
        </div>
      </div>
      <ArrowRight className="size-4 shrink-0 text-mut" />
    </button>
  );
}
