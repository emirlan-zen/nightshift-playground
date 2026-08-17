import { useNavigate } from "react-router-dom";
import { Plus, Radio } from "lucide-react";
import { useFlows, useHealth, useObs, useProposals, useServers, useTickets } from "@/lib/queries";
import { FlowCard } from "@/components/FlowCard";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, Kicker } from "@/components/ui/card";
import { EmptyText, SectionHeader } from "@/components/SectionHeader";

export function Home() {
  const navigate = useNavigate();
  const { data: flows = [] } = useFlows();
  const { data: servers = [] } = useServers();
  const { data: tickets = [] } = useTickets();
  const { data: obs } = useObs();
  const { data: proposals = [] } = useProposals();
  const { data: health } = useHealth();

  const running = flows.filter((f) => f.status === "running" || f.status === "queued");
  const blocked = flows.filter((f) => f.status === "blocked" || f.status === "deadline");
  const completed = flows.filter((f) => f.status === "complete").slice(0, 3);
  const manual = servers
    .flatMap((s) => (s.sessions ?? []).map((x) => ({ ...x, agent: s.company })))
    .filter((s) => s.active);
  const review = tickets
    .flatMap((group) => group.tickets ?? [])
    .filter((t) => t.status === "review");
  const healthIssues =
    (health?.auth.checkedAt && !health.auth.ok ? 1 : 0) +
    (health?.codex?.checkedAt && !health.codex.ok ? 1 : 0) +
    (health?.recentFailures?.length ?? 0) +
    (health?.forge?.filter((forge) => !forge.ok).length ?? 0) +
    (health?.depSkips?.length ?? 0);
  const inbox =
    blocked.length + review.length + proposals.length + (obs?.alerts.length ?? 0) + healthIssues;
  const budgetPct = obs?.budget.nightBudgetUSD
    ? Math.round((obs.budget.nightSpendUSD / obs.budget.nightBudgetUSD) * 100)
    : 0;
  const glance = [
    { label: "runs", value: running.length, hint: "active", to: "/runs" },
    { label: "sessions", value: manual.length, hint: "live", to: "/sessions" },
    { label: "inbox", value: inbox, hint: "open", to: "/inbox" },
    { label: "budget", value: `${budgetPct}%`, hint: "tonight", to: "/health" },
  ];

  return (
    <section>
      <div className="mb-3 flex items-center justify-between gap-3">
        <Kicker>// home · live now</Kicker>
        <Button variant="accent" size="md" onClick={() => navigate("/runs/new")}>
          <Plus className="size-4" /> New run
        </Button>
      </div>

      <Card className="mb-5 grid grid-cols-2 sm:grid-cols-4" data-testid="glance-strip">
        {glance.map((stat) => (
          <button
            key={stat.label}
            className="min-w-0 border-r border-line px-2 py-2.5 text-left transition-colors last:border-r-0 hover:bg-raise"
            aria-label={`${stat.label}: ${stat.value} ${stat.hint}`}
            onClick={() => navigate(stat.to)}
          >
            <span className="block truncate font-mono text-[8px] uppercase tracking-wider text-dim">
              {stat.label}
            </span>
            <span className="mt-0.5 block font-head text-[20px] font-bold leading-none tracking-tight tabular-nums text-ink">
              {stat.value}
            </span>
            <span className="mt-1 block font-mono text-[8px] uppercase tracking-wider text-mut">
              {stat.hint}
            </span>
            {stat.label === "budget" && (
              <span className="mt-1.5 block h-0.5 bg-bg2" aria-hidden="true">
                <span
                  className="block h-full bg-accent"
                  style={{ width: `${Math.min(100, Math.max(2, budgetPct))}%` }}
                />
              </span>
            )}
          </button>
        ))}
      </Card>

      <div className="mb-5">
        <h2 className="max-w-[18ch] font-head text-[22px] font-bold leading-tight tracking-tight sm:text-[26px]">
          Work without babysitting it.
        </h2>
        <p className="mt-1.5 max-w-[44ch] text-[12px] leading-relaxed text-mut sm:text-[13px]">
          Start a goal, set a finish time, and let fresh sessions plan, build, review, and validate
          it in an isolated worktree.
        </p>
      </div>

      <SectionHeader className="mb-3" title="In progress" meta={`${running.length} active`} />
      <div className="grid gap-3 lg:grid-cols-2">
        {running.length ? (
          running.map((f) => (
            <FlowCard key={f.id} flow={f} onOpen={() => navigate(`/runs/${f.id}`)} />
          ))
        ) : (
          <Card className="col-span-full p-4">
            <EmptyText>No unattended work running.</EmptyText>
          </Card>
        )}
      </div>

      <div className="mt-8 grid gap-5 md:grid-cols-2">
        <div>
          <SectionHeader
            className="mb-3"
            title="Manual sessions"
            meta="separate workspaces"
            action={
              <Button size="sm" onClick={() => navigate("/sessions")}>
                Manage
              </Button>
            }
          />
          <Card className="px-4 py-1">
            {manual.length ? (
              manual.map((s) => (
                <div
                  key={s.instance}
                  className="flex items-center gap-3 border-b border-line/60 py-3 last:border-0"
                >
                  <Radio className="size-3.5 text-ok" />
                  <span className="min-w-0 flex-1 truncate text-[13px] font-semibold">
                    {s.agent}
                    {s.slot ? ` · ${s.slot}` : ""}
                  </span>
                  <Badge tone="ok">live</Badge>
                </div>
              ))
            ) : (
              <EmptyText>No manual sessions running.</EmptyText>
            )}
          </Card>
        </div>
        <div>
          <SectionHeader
            className="mb-3"
            title="Recently completed"
            action={
              <Button size="sm" onClick={() => navigate("/runs")}>
                All runs
              </Button>
            }
          />
          <div className="space-y-2">
            {completed.length ? (
              completed.map((f) => (
                <FlowCard key={f.id} compact flow={f} onOpen={() => navigate(`/runs/${f.id}`)} />
              ))
            ) : (
              <Card className="p-4">
                <EmptyText>No completed runs yet.</EmptyText>
              </Card>
            )}
          </div>
        </div>
      </div>
    </section>
  );
}
