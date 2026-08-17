import { useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { api, type Job, type Report, type Pipeline, type PipelineWave } from "@/lib/api";
import { qk, useServers, useJobs, useObs, usePipeline, useFlows } from "@/lib/queries";
import { fmtAt, fmtLeft, fmtRunId, nightLabel, nextNight, shortModel } from "@/lib/format";
import {
  nightsFor,
  jobStatus,
  jobsForWave,
  aggregateStatus,
  indexFlowReports,
  type RunStatus,
  type NightGroup,
  type FlowReportMeta,
} from "@/lib/night";
import { Card } from "@/components/ui/card";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmButton } from "@/components/ui/confirm-button";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/field";
import { ClaudeBot } from "@/components/ClaudeBot";
import { ExecutorIcon } from "@/components/ExecutorIcon";
import { EmptyText, ErrorText } from "@/components/SectionHeader";
import { cn } from "@/lib/utils";

function SubLabel({ children }: { children: ReactNode }) {
  return (
    <div className="mb-[9px] mt-4 font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-mut first:mt-0.5">
      {children}
    </div>
  );
}

// A finished night run's obs status (1.6): plain "done" hid whether it
// delivered a report, was auth-skipped, or produced nothing.
const doneStatus: Record<string, { tone: BadgeProps["tone"]; label: string }> = {
  delivered: { tone: "ok", label: "delivered" },
  "auth-skip": { tone: "muted", label: "auth-skip" },
  "no-deliverable": { tone: "danger", label: "no report" },
};

function JobRow({
  job,
  agent,
  obsStatus,
  hasReport,
}: {
  job: Job;
  agent: string;
  obsStatus?: string;
  hasReport?: boolean;
}) {
  const qc = useQueryClient();
  const invalidate = () => qc.invalidateQueries({ queryKey: qk.jobs(agent) });
  const cancel = useMutation({
    mutationFn: () => api.cancelJob(agent, job.id),
    onSettled: invalidate,
  });
  const stop = useMutation({
    mutationFn: () => api.stopRun(agent, job.id),
    onSettled: invalidate,
  });

  const isRun = job.started && job.runState === "active";
  const title = job.label
    ? job.label
    : job.kind === "sweep"
      ? "sweep"
      : job.prompt.slice(0, 90) + (job.prompt.length > 90 ? "…" : "");
  const sub = [fmtAt(job.at), job.kind];
  if (job.model) sub.push(shortModel(job.model));
  if (job.effort) sub.push(job.effort);
  // live auto-stop countdown on a running run — the RC-session TTL, for runs (1.7)
  if (isRun && job.startedAt) {
    const stopAt = Date.parse(job.startedAt) / 1000 + (job.minutes || 480) * 60;
    sub.push(`auto-stop ${fmtLeft(stopAt)}`);
  }
  // hasReport wins over a lagging obs ingest so a delivered run reads "delivered"
  // as soon as its report file lands.
  const done =
    !isRun && job.started
      ? hasReport
        ? doneStatus.delivered
        : doneStatus[obsStatus ?? ""]
      : undefined;

  return (
    <div className="flex items-start gap-2.5 border-t border-line py-[11px] first:border-t-0">
      <ExecutorIcon
        executor={job.executor}
        className={cn(
          "mt-[3px] size-3.5 shrink-0",
          job.executor === "codex" ? "text-ink" : "text-mut",
        )}
      />
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] font-semibold leading-snug tracking-tight break-words">
          {title}
        </div>
        <div className="mt-1 font-mono text-[10.5px] tracking-wide text-dim">{sub.join(" · ")}</div>
      </div>
      <div className="flex flex-none items-center gap-1.5">
        {job.skipped ? (
          <Badge tone="danger">dep-skip</Badge>
        ) : job.gated && !job.started ? (
          <Badge tone="sched">waiting</Badge>
        ) : !job.started ? (
          <Badge tone="sched">scheduled</Badge>
        ) : isRun ? (
          <Badge tone="ok">running</Badge>
        ) : done ? (
          <Badge tone={done.tone}>{done.label}</Badge>
        ) : (
          <Badge tone="done">done</Badge>
        )}
        {!job.started && !job.skipped ? (
          <Button size="sm" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
            Cancel
          </Button>
        ) : isRun ? (
          <ConfirmButton
            label="Stop"
            pendingLabel="Stopping…"
            pending={stop.isPending}
            onConfirm={() => stop.mutate()}
          />
        ) : null}
      </div>
    </div>
  );
}

// report banner_tone -> chip color, matching the banner accents (banner.go):
// shipped=green, partial=amber, quiet=slate, stall=crimson.
const toneChip: Record<string, BadgeProps["tone"]> = {
  shipped: "ok",
  partial: "sched",
  quiet: "muted",
  stall: "danger",
};

// A flow node's verdict -> chip color (ADR-0017 verdict vocabulary): a passing
// verdict is green, needs-work is amber, blocked is crimson.
const verdictChip: Record<string, BadgeProps["tone"]> = {
  ok: "ok",
  complete: "ok",
  "needs-work": "sched",
  blocked: "danger",
};

// A reports-list row (1.1). Surfaces the report's headline + tone so the morning
// scan is glanceable. For a flow node's report (audit P1-3) it also names the
// owning run — goal, template, node role, and the node's verdict — so flow rows
// no longer collapse to an indistinct "Flow · <date>" fallback. Non-flow reports
// keep the humanized run-id fallback.
export function ReportRow({
  report: r,
  flow,
  onOpen,
}: {
  report: Report;
  flow?: FlowReportMeta;
  onOpen: () => void;
}) {
  const h = fmtRunId(r.id);
  // Headline (banner frontmatter) wins the title; a flow report without one
  // names its full goal instead of the bare run id (the CSS truncates it to the
  // available width); everything else keeps h.label. The short goal is only for
  // the secondary sub-line, where density matters.
  const title = r.headline || flow?.goal || h.label;
  const goalShort = flow
    ? flow.goal.length > 52
      ? flow.goal.slice(0, 51) + "…"
      : flow.goal
    : null;
  const sub = [
    // keep the run-id label under a non-flow headline (existing behavior)
    r.headline && !flow ? h.label : null,
    flow?.template,
    // when the goal is already the title, don't repeat it in the sub
    r.headline ? goalShort : null,
    // the node id is the authoritative label for a flow report (r.wave for others)
    flow ? flow.node : r.wave,
    r.stats,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <button
      onClick={onOpen}
      className="flex w-full items-start gap-2.5 border-t border-line py-3 text-left transition-opacity first:border-t-0 hover:opacity-70"
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          {r.tone && <Badge tone={toneChip[r.tone] ?? "muted"}>{r.tone}</Badge>}
          {flow?.verdict && (
            <Badge tone={verdictChip[flow.verdict] ?? "muted"}>{flow.verdict}</Badge>
          )}
          <span className="min-w-0 truncate text-[13px] font-semibold leading-snug tracking-tight">
            {title}
          </span>
        </div>
        {sub && (
          <div className="mt-1 truncate font-mono text-[10px] tracking-wide text-dim">{sub}</div>
        )}
      </div>
      {!!r.mtime && (
        <span className="flex-none font-mono text-[10.5px] text-dim">{fmtAt(r.mtime * 1000)}</span>
      )}
      <span className="flex-none font-mono text-[10px] uppercase tracking-wider text-mut">
        read →
      </span>
    </button>
  );
}

// A wave's aggregate status -> chip classes. running/scheduled/no-report earn
// color; the happy path and idle stay quiet so the eye lands on what's off.
const waveChipClass: Record<RunStatus | "idle", string> = {
  running: "border-ok text-ok",
  delivered: "border-ok/50 text-ok/90",
  scheduled: "border-sched/60 text-sched",
  waiting: "border-sched/60 text-sched", // gated, upstream not yet delivered
  "no-report": "border-danger/70 text-danger",
  "dep-skip": "border-danger/70 text-danger",
  "auth-skip": "border-line2 text-mut",
  done: "border-line2 text-dim",
  idle: "border-dashed border-line text-dim/60",
};

// Compact wave timeline for playground (2.2): each of the pipeline's waves as a
// pill, colored by the status of the run(s) that carried it tonight. Replaces
// the hand-maintained prose footer — the schedule now comes from /api/pipeline.
function WaveTimeline({
  waves,
  jobs,
  runStatus,
  reportIds,
}: {
  waves: PipelineWave[];
  jobs: Job[];
  runStatus: Map<string, string>;
  reportIds: Set<string>;
}) {
  return (
    <div className="mt-1 flex flex-wrap gap-1.5">
      {waves.map((wave) => {
        const wjobs = jobsForWave(wave, jobs);
        const st = aggregateStatus(
          wjobs.map((j) => jobStatus(j, reportIds.has(j.id), runStatus.get(j.id))),
        );
        return (
          <span
            key={wave.name}
            title={`${wave.time} · ${st}${wave.executor === "codex" ? " · codex executor" : ""}${wjobs.length > 1 ? ` · ${wjobs.length} runs` : ""}`}
            className={cn(
              "inline-flex items-center gap-1 border px-1.5 py-[3px] font-mono text-[10px] tracking-wide",
              waveChipClass[st],
            )}
          >
            {st === "running" && (
              <span className="size-1.5 animate-pulse rounded-full bg-current" />
            )}
            {wave.name}
            {wjobs.length > 1 && <span className="opacity-70">×{wjobs.length}</span>}
          </span>
        );
      })}
    </div>
  );
}

// One night's block: header + (playground) wave timeline + run rows + reports.
function NightSection({
  night,
  agent,
  pipeline,
  runStatus,
  reportIds,
  flowReports,
  onOpenReport,
}: {
  night: NightGroup;
  agent: string;
  pipeline?: Pipeline;
  runStatus: Map<string, string>;
  reportIds: Set<string>;
  flowReports: Map<string, FlowReportMeta>;
  onOpenReport: (id: string) => void;
}) {
  // A compact per-night rollup from the run statuses.
  const tally = { delivered: 0, running: 0, scheduled: 0 };
  for (const j of night.jobs) {
    const s = jobStatus(j, reportIds.has(j.id), runStatus.get(j.id));
    if (s === "delivered") tally.delivered++;
    else if (s === "running") tally.running++;
    else if (s === "scheduled") tally.scheduled++;
  }
  const summary = [
    tally.running ? `${tally.running} running` : null,
    tally.delivered ? `${tally.delivered} delivered` : null,
    tally.scheduled ? `${tally.scheduled} scheduled` : null,
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <div className="mt-4 border-t border-line pt-3.5 first:mt-0 first:border-t-0 first:pt-0">
      <div className="flex items-baseline justify-between gap-3">
        <span className="font-head text-[13.5px] font-bold tracking-tight">
          {nightLabel(night.key)}
        </span>
        {summary && <span className="font-mono text-[10px] text-dim">{summary}</span>}
      </div>

      {pipeline && night.jobs.length > 0 && (
        <WaveTimeline
          waves={pipeline.waves}
          jobs={night.jobs}
          runStatus={runStatus}
          reportIds={reportIds}
        />
      )}

      {night.jobs.length > 0 && (
        <>
          <SubLabel>Runs</SubLabel>
          {night.jobs.map((j) => (
            <JobRow
              key={j.id}
              job={j}
              agent={agent}
              obsStatus={runStatus.get(j.id)}
              hasReport={reportIds.has(j.id)}
            />
          ))}
        </>
      )}

      {night.reports.length > 0 && (
        <>
          <SubLabel>Reports</SubLabel>
          {night.reports.map((r) => (
            <ReportRow
              key={r.id}
              report={r}
              flow={flowReports.get(r.id)}
              onOpen={() => onOpenReport(r.id)}
            />
          ))}
        </>
      )}
    </div>
  );
}

function AgentNight({
  agent,
  sweepOn,
  pipeline,
}: {
  agent: string;
  sweepOn: boolean;
  pipeline?: Pipeline;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [composing, setComposing] = useState(false);
  const [prompt, setPrompt] = useState("");
  const [at, setAt] = useState(nextNight);
  const [effort, setEffort] = useState("");
  const [executor, setExecutor] = useState("");
  const [minutes, setMinutes] = useState("");

  const jobs = useJobs(agent);
  const reports = useQuery({
    queryKey: qk.reports(agent),
    queryFn: () => api.reports(agent),
  });
  // obs run statuses, to distinguish delivered / auth-skip / no-deliverable on
  // finished job rows (1.6). One shared cache for all agents on the page.
  const { data: obs } = useObs();
  const runStatus = new Map(
    (obs?.runs ?? []).filter((r) => r.agent === agent).map((r) => [r.runId, r.status]),
  );
  const reportIds = new Set((reports.data ?? []).map((r) => r.id));
  // Join flow reports to their run so night-history rows name the flow + verdict
  // (audit P1-3). Report ids are globally unique, so the box-wide index is safe.
  const { data: flows } = useFlows();
  const flowReports = indexFlowReports(flows ?? []);

  const sweep = useMutation({
    mutationFn: (on: boolean) => api.setSweep(agent, on),
    onMutate: async (on) => {
      await qc.cancelQueries({ queryKey: qk.sweeps });
      const prev = qc.getQueryData<Record<string, boolean>>(qk.sweeps);
      qc.setQueryData<Record<string, boolean>>(qk.sweeps, (m) => ({
        ...(m ?? {}),
        [agent]: on,
      }));
      return { prev };
    },
    onError: (_e, _on, ctx) => ctx?.prev && qc.setQueryData(qk.sweeps, ctx.prev),
    onSettled: () => qc.invalidateQueries({ queryKey: qk.sweeps }),
  });

  const queue = useMutation({
    mutationFn: () =>
      api.queueJob(agent, prompt.trim(), at, {
        effort: effort || undefined,
        executor: executor || undefined,
        minutes: minutes ? Number(minutes) : undefined,
      }),
    onSuccess: () => {
      setPrompt("");
      setEffort("");
      setExecutor("");
      setMinutes("");
      setComposing(false);
      qc.invalidateQueries({ queryKey: qk.jobs(agent) });
    },
  });

  const list = jobs.data ?? [];
  const running = list.some((j) => j.started && j.runState === "active");
  const nights = nightsFor(list, reports.data ?? []);

  return (
    <Card className="mb-3">
      <div className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-[15px]">
        <div className="flex items-center gap-2.5 font-head text-[17px] font-bold tracking-tight">
          <span
            className={cn(
              "size-[7px] rounded-full",
              running
                ? "bg-ok shadow-[0_0_0_3px_color-mix(in_srgb,var(--color-ok)_22%,transparent)]"
                : "bg-dim",
            )}
          />
          {agent}
          {running && <ClaudeBot active size={22} />}
        </div>
        <div className="ml-auto flex items-center gap-3">
          <label className="flex cursor-pointer select-none items-center gap-2 font-mono text-[10px] uppercase tracking-wider text-mut">
            <Switch
              checked={sweepOn}
              disabled={sweep.isPending}
              onCheckedChange={(v) => sweep.mutate(v)}
            />
            Sweep
          </label>
          <Button size="sm" onClick={() => setComposing((v) => !v)}>
            <Plus className="size-3" /> Run
          </Button>
        </div>
      </div>

      <div className="px-4 py-3.5">
        {composing && (
          <div className="mb-3.5 border border-dashed border-line2 p-3.5">
            <Textarea
              autoFocus
              placeholder={`Queue a night run for ${agent}…`}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
            />
            <div className="mt-2.5 flex items-stretch gap-2.5">
              <select
                aria-label="Reasoning effort"
                value={effort}
                onChange={(e) => setEffort(e.target.value)}
                className="min-w-0 flex-1 border border-line2 bg-bg px-3 py-2.5 font-mono text-[12px] uppercase tracking-wider text-ink focus:border-accent focus:outline-none"
              >
                <option value="">effort: default</option>
                {["low", "medium", "high", "xhigh", "max"].map((e) => (
                  <option key={e} value={e}>
                    {e}
                  </option>
                ))}
              </select>
              <select
                aria-label="Executor"
                value={executor}
                onChange={(e) => setExecutor(e.target.value)}
                title="claude = Claude Code (attachable); codex = OpenAI Codex CLI, gpt-5.6-sol, report-only (ADR-0018)"
                className="min-w-0 flex-1 border border-line2 bg-bg px-3 py-2.5 font-mono text-[12px] uppercase tracking-wider text-ink focus:border-accent focus:outline-none"
              >
                <option value="">executor: claude</option>
                <option value="codex">codex</option>
              </select>
              <input
                type="number"
                min={10}
                max={480}
                placeholder="auto-stop min"
                value={minutes}
                onChange={(e) => setMinutes(e.target.value)}
                className="w-[128px] flex-none border border-line2 bg-bg px-3 py-2.5 font-mono text-[13px] text-ink focus:border-accent focus:outline-none"
              />
            </div>
            <div className="mt-2.5 flex items-stretch gap-2.5">
              <input
                type="datetime-local"
                value={at}
                onChange={(e) => setAt(e.target.value)}
                className="min-w-0 flex-1 border border-line2 bg-bg px-3 py-2.5 font-mono text-[13px] text-ink focus:border-accent focus:outline-none"
              />
              <Button
                variant="accent"
                disabled={!prompt.trim() || queue.isPending}
                onClick={() => queue.mutate()}
              >
                Queue
              </Button>
            </div>
            <ErrorText>{queue.error instanceof Error ? queue.error.message : null}</ErrorText>
          </div>
        )}

        {nights.length === 0 ? (
          <EmptyText>No queued or running jobs.</EmptyText>
        ) : (
          nights.map((n) => (
            <NightSection
              key={n.key}
              night={n}
              agent={agent}
              pipeline={pipeline}
              runStatus={runStatus}
              reportIds={reportIds}
              flowReports={flowReports}
              onOpenReport={(id) => navigate(`/night/${agent}/${encodeURIComponent(id)}`)}
            />
          ))
        )}
      </div>
    </Card>
  );
}

// The scheduler's per-agent view — sweeps, ad-hoc composer, and night-grouped
// jobs + reports + wave timeline. Folded into the Runs surface (ADR-0020): the
// old standalone /night page is gone, this is now a section of /runs.
export function NightHistory() {
  const { data: servers } = useServers();
  const sweeps = useQuery({ queryKey: qk.sweeps, queryFn: api.sweeps });
  const { data: pipeline } = usePipeline();
  const agents = (servers ?? []).map((s) => s.company);

  return (
    <div>
      {agents.map((a) => (
        <AgentNight
          key={a}
          agent={a}
          sweepOn={!!sweeps.data?.[a]}
          pipeline={a === "playground" ? pipeline : undefined}
        />
      ))}
      <div className="mt-[30px] max-w-[64ch] text-[11px] leading-relaxed text-dim">
        Night runs auto-stop 8h after start (per-run override above). Company sweeps run ~23:20
        Bishkek. Playground runs the {pipeline?.waves.length ?? "multi"}-wave nightly pipeline shown
        on the timeline
        {pipeline?.source === "profile" ? ` (profile: ${pipeline.profile})` : ""}
        {pipeline?.source === "legacy" ? " (pipeline.json)" : ""}. Sessions stay attachable in the
        Claude app.
      </div>
    </div>
  );
}
