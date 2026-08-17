import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import {
  AlertTriangle,
  ArrowRight,
  CheckCheck,
  FileCode2,
  GitPullRequest,
  ShieldAlert,
} from "lucide-react";
import { api } from "@/lib/api";
import {
  qk,
  useChangeProposals,
  useFlows,
  useHealth,
  useObs,
  useProposals,
  useTickets,
} from "@/lib/queries";
import { fmtAt, fmtRunId } from "@/lib/format";
import { FlowCard } from "@/components/FlowCard";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, Kicker } from "@/components/ui/card";
import { EmptyText, SectionHeader } from "@/components/SectionHeader";

export function Inbox() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: flows = [] } = useFlows();
  const { data: tickets = [] } = useTickets();
  const { data: health } = useHealth();
  const { data: obs } = useObs();
  const { data: proposals = [] } = useProposals();
  const blocked = flows.filter((f) => f.status === "blocked" || f.status === "deadline");
  const review = tickets.flatMap((g) => g.tickets ?? []).filter((t) => t.status === "review");

  const { data: changeProposals = [] } = useChangeProposals();

  const refreshProposals = () =>
    qc
      .invalidateQueries({ queryKey: qk.proposals })
      .then(() => qc.invalidateQueries({ queryKey: qk.profiles }));
  const apply = useMutation({ mutationFn: api.applyProposal, onSuccess: refreshProposals });
  const dismiss = useMutation({ mutationFn: api.dismissProposal, onSuccess: refreshProposals });

  const refreshChangeProposals = () =>
    qc
      .invalidateQueries({ queryKey: qk.changeProposals })
      .then(() => qc.invalidateQueries({ queryKey: qk.flowCatalog }));
  const applyChange = useMutation({
    mutationFn: api.applyChangeProposal,
    onSuccess: refreshChangeProposals,
  });
  const dismissChange = useMutation({
    mutationFn: api.dismissChangeProposal,
    onSuccess: refreshChangeProposals,
  });

  const incidents: { key: string; title: string; detail: string; tone: "danger" | "review" }[] = [];
  if (health?.auth.checkedAt && !health.auth.ok)
    incidents.push({
      key: "auth",
      title: "Claude authentication is down",
      detail: health.auth.detail,
      tone: "danger",
    });
  if (health?.codex?.checkedAt && !health.codex.ok)
    incidents.push({
      key: "codex-auth",
      title: "Codex authentication is down (review executor)",
      detail: health.codex.detail,
      tone: "danger",
    });
  for (const failure of health?.recentFailures ?? [])
    incidents.push({
      key: `auth-${failure.agent}-${failure.id}`,
      title: `${failure.agent} run skipped — authentication`,
      detail: fmtRunId(failure.id).label,
      tone: "danger",
    });
  for (const skip of health?.depSkips ?? [])
    incidents.push({
      key: `dep-${skip.agent}-${skip.id}`,
      title: `${skip.agent} stage dependency-skipped`,
      detail: skip.reason,
      tone: "danger",
    });
  for (const forge of health?.forge?.filter((f) => !f.ok) ?? [])
    incidents.push({
      key: `forge-${forge.company}`,
      title: `${forge.company} forge credential rejected`,
      detail: forge.detail,
      tone: "review",
    });
  for (const alert of obs?.alerts ?? [])
    incidents.push({
      key: `obs-${alert.agent}-${alert.kind}-${alert.ts}`,
      title: `${alert.agent} · ${alert.kind}`,
      detail: alert.detail,
      tone: "review",
    });

  const total =
    blocked.length + review.length + proposals.length + changeProposals.length + incidents.length;
  return (
    <section>
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <Kicker>// operator inbox</Kicker>
          <h1 className="mt-2 font-head text-[27px] font-bold tracking-tight">
            Decisions, not noise.
          </h1>
          <p className="mt-1 max-w-[62ch] text-[13px] leading-relaxed text-mut">
            Blocked runs, review-ready work, automation proposals, and system incidents share one
            queue.
          </p>
        </div>
        <Badge tone={total ? "review" : "ok"}>{total ? `${total} open` : "clear"}</Badge>
      </div>

      <div className="grid gap-6 xl:grid-cols-2">
        <div className="space-y-6">
          <div>
            <SectionHeader className="mb-3" title="Run decisions" meta={`${blocked.length}`} />
            <div className="space-y-2">
              {blocked.length ? (
                blocked.map((flow) => (
                  <FlowCard
                    key={flow.id}
                    compact
                    flow={flow}
                    onOpen={() => navigate(`/runs/${flow.id}`)}
                  />
                ))
              ) : (
                <Card className="px-4">
                  <EmptyText>No blocked or expired runs.</EmptyText>
                </Card>
              )}
            </div>
          </div>

          <div>
            <SectionHeader
              className="mb-3"
              title="Ready for review"
              meta={`${review.length}`}
              action={
                <Button size="sm" onClick={() => navigate("/tickets")}>
                  Ticket board
                </Button>
              }
            />
            <Card className="px-4 py-1">
              {review.length ? (
                review.map((ticket) => (
                  <button
                    key={`${ticket.agent}-${ticket.id}`}
                    onClick={() =>
                      navigate(`/tickets/${ticket.agent}/${encodeURIComponent(ticket.id)}`)
                    }
                    className="flex min-h-14 w-full items-center gap-3 border-b border-line/60 py-3 text-left last:border-0"
                  >
                    <CheckCheck className="size-4 shrink-0 text-review" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[13px] font-semibold">
                        {ticket.title}
                      </span>
                      <span className="mt-0.5 block font-mono text-[9.5px] uppercase text-dim">
                        {ticket.agent} · {fmtAt(ticket.updated)}
                      </span>
                    </span>
                    <ArrowRight className="size-4 text-dim" />
                  </button>
                ))
              ) : (
                <EmptyText>No tickets waiting for operator review.</EmptyText>
              )}
            </Card>
          </div>
        </div>

        <div className="space-y-6">
          <div>
            <SectionHeader
              className="mb-3"
              title="Automation proposals"
              meta={`${proposals.length}`}
              action={
                <Button size="sm" onClick={() => navigate("/automations")}>
                  Studio
                </Button>
              }
            />
            <Card className="px-4 py-1">
              {proposals.length ? (
                proposals.map((proposal) => (
                  <div
                    key={proposal.name}
                    className="flex flex-wrap items-center gap-2 border-b border-line/60 py-3 last:border-0"
                  >
                    <GitPullRequest className="size-4 shrink-0 text-review" />
                    <div className="min-w-0 flex-1">
                      <div className="text-[13px] font-semibold">{proposal.name}</div>
                      <div className="mt-0.5 text-[11px] text-mut">
                        {proposal.why || proposal.error || "Agent-proposed schedule change"}
                      </div>
                    </div>
                    {proposal.valid && (
                      <Button
                        size="sm"
                        variant="accent"
                        disabled={apply.isPending}
                        onClick={() => apply.mutate(proposal.name)}
                      >
                        Apply
                      </Button>
                    )}
                    <Button
                      size="sm"
                      disabled={dismiss.isPending}
                      onClick={() => dismiss.mutate(proposal.name)}
                    >
                      Dismiss
                    </Button>
                  </div>
                ))
              ) : (
                <EmptyText>No automation proposals.</EmptyText>
              )}
            </Card>
          </div>

          <div>
            <SectionHeader
              className="mb-3"
              title="Definition proposals"
              meta={`${changeProposals.length}`}
              action={
                <Button size="sm" onClick={() => navigate("/automations")}>
                  Studio
                </Button>
              }
            />
            <Card className="px-4 py-1">
              {changeProposals.length ? (
                changeProposals.map((proposal) => (
                  <div
                    key={proposal.name}
                    role="group"
                    aria-label={`Definition proposal ${proposal.name}`}
                    className="border-b border-line/60 py-3 last:border-0"
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <FileCode2 className="size-4 shrink-0 text-review" />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-[13px] font-semibold">{proposal.name}</span>
                          <Badge tone="muted">
                            {proposal.type}
                            {proposal.target ? ` · ${proposal.target}` : ""}
                          </Badge>
                          {!proposal.valid && <Badge tone="danger">invalid</Badge>}
                        </div>
                        <div className="mt-0.5 text-[11px] leading-relaxed text-mut">
                          {proposal.why || proposal.error}
                        </div>
                        {!proposal.valid && proposal.error && (
                          <div className="mt-0.5 font-mono text-[10px] text-danger">
                            {proposal.error}
                          </div>
                        )}
                      </div>
                      {proposal.valid && (
                        <Button
                          size="sm"
                          variant="accent"
                          disabled={applyChange.isPending}
                          onClick={() => applyChange.mutate(proposal.name)}
                        >
                          Apply
                        </Button>
                      )}
                      <Button
                        size="sm"
                        disabled={dismissChange.isPending}
                        onClick={() => dismissChange.mutate(proposal.name)}
                      >
                        Dismiss
                      </Button>
                    </div>
                    {proposal.valid && (proposal.body || proposal.template || proposal.def) && (
                      <details className="mt-2">
                        <summary className="flex min-h-10 cursor-pointer items-center font-mono text-[9.5px] uppercase tracking-wider text-dim">
                          Compare current → proposed
                        </summary>
                        <div className="mt-2 grid gap-2 lg:grid-cols-2">
                          <pre className="max-h-[220px] overflow-auto whitespace-pre-wrap border border-line bg-bg p-2 font-mono text-[10px] leading-relaxed text-dim">
                            {proposal.current || "(new)"}
                          </pre>
                          <pre className="max-h-[220px] overflow-auto whitespace-pre-wrap border border-accent/40 bg-bg p-2 font-mono text-[10px] leading-relaxed text-mut">
                            {proposal.body ??
                              JSON.stringify(proposal.template ?? proposal.def, null, 2)}
                          </pre>
                        </div>
                      </details>
                    )}
                  </div>
                ))
              ) : (
                <EmptyText>No node or template proposals.</EmptyText>
              )}
            </Card>
          </div>

          <div>
            <SectionHeader
              className="mb-3"
              title="Incidents"
              meta={`${incidents.length}`}
              action={
                <Button size="sm" onClick={() => navigate("/health")}>
                  Health
                </Button>
              }
            />
            <Card className="px-4 py-1">
              {incidents.length ? (
                incidents.map((incident) => (
                  <div
                    key={incident.key}
                    className="flex gap-3 border-b border-line/60 py-3 last:border-0"
                  >
                    {incident.tone === "danger" ? (
                      <ShieldAlert className="mt-0.5 size-4 shrink-0 text-danger" />
                    ) : (
                      <AlertTriangle className="mt-0.5 size-4 shrink-0 text-review" />
                    )}
                    <div className="min-w-0">
                      <div className="text-[13px] font-semibold">{incident.title}</div>
                      <div className="mt-1 break-words font-mono text-[10px] leading-relaxed text-mut">
                        {incident.detail}
                      </div>
                    </div>
                  </div>
                ))
              ) : (
                <EmptyText>No active system incidents.</EmptyText>
              )}
            </Card>
          </div>
        </div>
      </div>
    </section>
  );
}
