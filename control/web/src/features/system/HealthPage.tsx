import type { ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { type ObsAlert, type ObsRun, type ObsAgentStat, type Vitals } from "@/lib/api";
import { useHealth, useObs, useVitals } from "@/lib/queries";
import { fmtAt, fmtInt, fmtBytes, fmtUptime } from "@/lib/format";
import { Card, Kicker } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { BudgetBar } from "@/components/BudgetBar";
import { ExecutorIcon } from "@/components/ExecutorIcon";
import { SectionHeader, EmptyText, ErrorText } from "@/components/SectionHeader";
import { cn } from "@/lib/utils";

// Observability tab (ADR-0006): open alerts, the night ledger, and per-agent
// health — derived from the transcript store. This is where the freeze /
// off-rails / silent-failure signals surface on the phone.

const alertTone: Record<string, "danger" | "sched" | "muted" | "accent"> = {
  stall: "danger",
  "error-storm": "danger",
  "no-deliverable": "danger",
  // limit-hit is the governor's calibration event (ADR-0012), not a failure —
  // give it the accent tone so it stands out from red alerts (1.4).
  "limit-hit": "accent",
  loop: "sched",
  // ADR-0023: a refused emission and an unparsable scores block are both "the
  // run asked for something the rules do not allow" — a caution, not a failure:
  // the verdict still routed and the run carried on.
  "emit-refused": "sched",
  "score-parse": "sched",
};

const statusTone: Record<string, "ok" | "danger" | "muted" | "accent"> = {
  delivered: "ok",
  "no-deliverable": "danger",
  "auth-skip": "muted",
  running: "accent",
};

// Row wrapper that becomes a link to the run's report when one exists (1.5), so
// the operator jumps straight from an alert/ledger line to the morning report
// instead of memorizing a run id across tabs.
function RunLink({
  agent,
  id,
  linked,
  children,
}: {
  agent: string;
  id?: string;
  linked: boolean;
  children: ReactNode;
}) {
  const navigate = useNavigate();
  const base = "flex items-center gap-3 border-b border-line/60 py-2.5 text-left last:border-0";
  if (!linked || !id) return <div className={base}>{children}</div>;
  return (
    <button
      className={`${base} w-full transition-opacity hover:opacity-70`}
      onClick={() => navigate(`/night/${agent}/${encodeURIComponent(id)}`)}
    >
      {children}
    </button>
  );
}

function AlertRow({ a }: { a: ObsAlert }) {
  const who = a.runId || a.sessionId || a.agent;
  return (
    <RunLink agent={a.agent} id={a.runId} linked={!!a.runId}>
      <Badge tone={alertTone[a.kind] ?? "muted"}>{a.kind}</Badge>
      <div className="min-w-0 flex-1">
        <div className="break-words text-[12.5px] text-ink">{a.detail}</div>
        <div className="mt-0.5 truncate font-mono text-[10px] text-dim">
          {a.agent} · {who}
        </div>
      </div>
      <span className="shrink-0 font-mono text-[10px] text-dim">{fmtAt(a.ts * 1000)}</span>
    </RunLink>
  );
}

function LedgerRow({ r }: { r: ObsRun }) {
  return (
    <RunLink agent={r.agent} id={r.runId} linked={r.report}>
      <span className="w-[52px] shrink-0 font-mono text-[10px] text-dim tabular-nums">
        {fmtAt(r.started * 1000)}
      </span>
      <span className="min-w-0 flex-1 truncate font-mono text-[12px]">
        {r.label || r.runId}
        <span className="ml-1.5 text-dim">· {r.agent}</span>
      </span>
      <Badge tone={statusTone[r.status] ?? "muted"}>{r.status}</Badge>
    </RunLink>
  );
}

// One auth/token row: claude auth or a company's forge (GitHub/GitLab) token.
function TokenRow({
  label,
  ok,
  detail,
  checkedAt,
}: {
  label: string;
  ok: boolean;
  detail: string;
  checkedAt: number;
}) {
  const executor = label === "claude" || label === "codex" ? label : undefined;
  return (
    <div className="flex items-start gap-3 border-b border-line/60 py-2.5 last:border-0">
      <span className="flex w-[92px] shrink-0 items-center gap-1.5 font-mono text-[12px] font-medium uppercase tracking-wider">
        {executor && (
          <ExecutorIcon
            executor={executor}
            className={cn("size-3.5", ok ? "text-ink" : "text-danger")}
          />
        )}
        {label}
      </span>
      <div className="min-w-0 flex-1 break-words font-mono text-[11px] text-mut">
        {detail || "—"}
      </div>
      <span className="shrink-0 font-mono text-[10px] text-dim">
        {checkedAt > 0 ? fmtAt(checkedAt * 1000) : "never"}
      </span>
      <Badge tone={checkedAt === 0 ? "muted" : ok ? "ok" : "danger"}>
        {checkedAt === 0 ? "unchecked" : ok ? "ok" : "dead"}
      </Badge>
    </div>
  );
}

// A used/total meter for a finite box resource (memory, disk). Turns danger red
// past `warn` so a filling disk / memory pressure reads at a glance.
function MeterBar({
  label,
  used,
  total,
  warn = 0.85,
}: {
  label: string;
  used: number;
  total: number;
  warn?: number;
}) {
  const frac = total > 0 ? used / total : 0;
  const pct = Math.round(frac * 100);
  const hot = frac >= warn;
  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-3">
        <span className="font-mono text-[12px] font-medium uppercase tracking-wider">{label}</span>
        <span className="font-mono text-[11px] text-mut tabular-nums">
          {fmtBytes(used)}
          <span className="text-dim"> / {fmtBytes(total)}</span>
        </span>
      </div>
      <div className="flex items-center gap-2">
        <div className="h-2 flex-1 bg-bg2">
          <div
            className={hot ? "h-full bg-danger" : "h-full bg-accent"}
            style={{ width: `${Math.min(100, Math.max(2, pct))}%` }}
          />
        </div>
        <span className="w-[38px] shrink-0 text-right font-mono text-[10px] text-dim tabular-nums">
          {pct}%
        </span>
      </div>
    </div>
  );
}

// The box's own vitals — load / memory / disk / uptime. This is the one health
// signal the transcript-derived obs store can't see: whether the VM itself is
// under memory pressure or the disk is filling from Docker layers + reports.
function MachineVitals({ v }: { v: Vitals }) {
  // load1 above the core count = the box is saturated / backing up.
  const loadHot = v.cpuCount > 0 && v.load1 > v.cpuCount;
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <div className="flex items-baseline gap-2">
          <span className="font-mono text-[12px] font-medium uppercase tracking-wider">load</span>
          <span
            className={`font-head text-[19px] font-bold tabular-nums ${loadHot ? "text-danger" : ""}`}
          >
            {v.load1.toFixed(2)}
          </span>
          <span className="font-mono text-[11px] text-dim tabular-nums">
            {v.load5.toFixed(2)} · {v.load15.toFixed(2)} · {v.cpuCount} cores
          </span>
        </div>
        <span className="font-mono text-[11px] text-mut tabular-nums">
          up {fmtUptime(v.uptimeSec)}
        </span>
      </div>
      <MeterBar label="mem" used={v.memUsedBytes} total={v.memTotalBytes} warn={0.9} />
      {v.swapTotalBytes > 0 && (
        <MeterBar label="swap" used={v.swapUsedBytes} total={v.swapTotalBytes} warn={0.5} />
      )}
      <MeterBar label="disk" used={v.diskUsedBytes} total={v.diskTotalBytes} warn={0.85} />
      {!v.ok && (
        <div className="font-mono text-[10.5px] text-dim">
          kernel counters unavailable — disk figures only
        </div>
      )}
    </div>
  );
}

function AgentHealth({ a }: { a: ObsAgentStat }) {
  const rate = a.toolCalls ? a.toolErrors / a.toolCalls : 0;
  const pct = Math.round(rate * 100);
  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-3">
        <span className="font-mono text-[12px] font-medium uppercase tracking-wider">
          {a.agent}
        </span>
        <span className="font-mono text-[11px] text-mut tabular-nums">
          {fmtInt(a.turns)} turns
          <span className="text-dim"> · {fmtInt(a.sessions)} sessions</span>
        </span>
      </div>
      <div className="flex items-center gap-2">
        <div className="h-2 flex-1 bg-bg2">
          <div
            className={pct >= 25 ? "h-full bg-danger" : "h-full bg-accent"}
            style={{ width: `${Math.min(100, Math.max(2, pct))}%` }}
          />
        </div>
        <span className="w-[74px] shrink-0 text-right font-mono text-[10px] text-dim tabular-nums">
          {pct}% err · {fmtInt(a.toolCalls)}
        </span>
      </div>
    </div>
  );
}

export function Health() {
  // Through the filtered hook, not a raw useQuery — presentation mode
  // (lib/visibility) is enforced in the query layer, and a page-local query
  // on the same key bypasses it (the leak the operator caught on 2026-08-15).
  const { data, error, isLoading } = useObs();
  const { data: health } = useHealth();
  const { data: vitals } = useVitals();

  if (isLoading) return <EmptyText>Loading observability…</EmptyText>;
  if (error instanceof Error) return <ErrorText>{error.message}</ErrorText>;
  if (!data) return null;

  return (
    <section>
      <div className="mb-4 flex items-baseline justify-between gap-3">
        <Kicker>// observability · from session transcripts</Kicker>
        <span className="font-mono text-[10px] text-dim">
          {data.alerts.length} open alert{data.alerts.length === 1 ? "" : "s"}
        </span>
      </div>

      <SectionHeader title="Machine" meta="hetzner cpx42 · load · mem · disk" />
      <Card className="mt-3 p-4">
        {vitals ? <MachineVitals v={vitals} /> : <EmptyText>Loading vitals…</EmptyText>}
      </Card>

      <SectionHeader className="mt-8" title="Night budget" meta="governor · API-equiv spend" />
      <Card className="mt-3 p-4">
        <BudgetBar b={data.budget} />
      </Card>

      <SectionHeader
        className="mt-8"
        title="Auth & tokens"
        meta="claude oauth · codex oauth · forge per company"
      />
      <Card className="mt-3 px-4 py-1">
        {health ? (
          <>
            <TokenRow
              label="claude"
              ok={health.auth.ok}
              detail={health.auth.detail}
              checkedAt={health.auth.checkedAt}
            />
            {health.codex && (
              // Codex executor auth (ADR-0018). Refreshed at launcher
              // pre-flight, not on a ticker — the timestamp carries the "as
              // of" meaning here.
              <TokenRow
                label="codex"
                ok={health.codex.ok}
                detail={health.codex.detail}
                checkedAt={health.codex.checkedAt}
              />
            )}
            {(health.forge ?? []).map((f) => (
              <TokenRow
                key={f.company}
                label={f.company}
                ok={f.ok}
                detail={f.detail}
                checkedAt={f.checkedAt}
              />
            ))}
          </>
        ) : (
          <EmptyText>Loading auth status…</EmptyText>
        )}
      </Card>

      <SectionHeader
        className="mt-8"
        title="Alerts"
        meta="loop · error-storm · stall · no-deliverable · limit-hit · emit-refused · score-parse"
      />
      <Card className="mt-3 px-4 py-1">
        {data.alerts.length === 0 ? (
          <EmptyText>No open alerts — nights running clean.</EmptyText>
        ) : (
          data.alerts.map((a, i) => <AlertRow key={a.kind + a.runId + a.sessionId + i} a={a} />)
        )}
      </Card>

      <SectionHeader className="mb-3 mt-8" title="Night ledger" meta="last 14 days" />
      <Card className="px-4 py-1">
        {data.runs.length === 0 ? (
          <EmptyText>No runs recorded.</EmptyText>
        ) : (
          data.runs.map((r) => <LedgerRow key={r.agent + r.runId} r={r} />)
        )}
      </Card>

      <SectionHeader className="mb-3 mt-8" title="Per-agent health" meta="tool-error rate" />
      <Card className="space-y-3 p-4">
        {data.agents.length === 0 ? (
          <EmptyText>No recent sessions.</EmptyText>
        ) : (
          data.agents.map((a) => <AgentHealth key={a.agent} a={a} />)
        )}
      </Card>

      <div className="mt-8 max-w-[64ch] text-[11px] leading-relaxed text-dim">
        Derived from each agent's Claude Code session transcripts — metadata only (tool names, error
        flags, timings; never message content). Alerts flag loops, error storms, frozen sessions,
        and runs that ended without a morning report — the signals a night run can't report about
        itself.
      </div>
    </section>
  );
}
