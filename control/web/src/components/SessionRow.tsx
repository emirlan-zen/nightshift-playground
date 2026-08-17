import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Job, type SessionState } from "@/lib/api";
import { qk } from "@/lib/queries";
import { fmtAt, fmtLeft, fmtRunId, shortModel } from "@/lib/format";
import { Badge } from "@/components/ui/badge";
import { ConfirmButton } from "@/components/ui/confirm-button";
import { ClaudeBot } from "@/components/ClaudeBot";
import { ExecutorIcon } from "@/components/ExecutorIcon";
import { cn } from "@/lib/utils";

// Shared visual shell for one live session — a remote-control server session or
// a night-run session. Both surface a Stop control here so the Servers tab is
// the single place to see and kill everything alive for an agent.
function SessionShell({
  active,
  kind,
  name,
  sub,
  executor,
  onStop,
  stopping,
}: {
  active: boolean;
  kind: "server" | "night";
  name: string;
  sub: string;
  executor?: string;
  onStop: () => void;
  stopping: boolean;
}) {
  return (
    <div className="flex items-center gap-2.5 border-t border-line py-[11px] first:border-t-0">
      {active ? (
        <ClaudeBot active size={20} className="flex-none" />
      ) : (
        <span className="size-[7px] flex-none rounded-full bg-dim" />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13.5px] font-semibold leading-snug tracking-tight">
            {name}
          </span>
          <ExecutorIcon executor={executor} className="size-[15px] text-mut" />
          <Badge tone={kind === "server" ? "accent" : "review"}>{kind}</Badge>
        </div>
        <div className="mt-1 font-mono text-[10.5px] tracking-wide text-dim">{sub}</div>
      </div>
      <ConfirmButton
        label="Stop"
        pendingLabel="Stopping…"
        pending={stopping}
        onConfirm={onStop}
        className={cn("flex-none", !active && "opacity-60")}
      />
    </div>
  );
}

export function RcSessionRow({ company, session }: { company: string; session: SessionState }) {
  const qc = useQueryClient();
  const stop = useMutation({
    mutationFn: () => api.sessionStop(company, session.instance),
    onSettled: () => qc.invalidateQueries({ queryKey: qk.status }),
  });
  const sub =
    session.active && session.ttl_until
      ? `auto-stop ${fmtLeft(session.ttl_until)}`
      : session.active
        ? "running"
        : "stopped";
  return (
    <SessionShell
      active={session.active}
      kind="server"
      name={session.slot || "default"}
      sub={sub}
      executor={session.executor}
      onStop={() => stop.mutate()}
      stopping={stop.isPending}
    />
  );
}

export function NightSessionRow({ agent, job }: { agent: string; job: Job }) {
  const qc = useQueryClient();
  const stop = useMutation({
    mutationFn: () => api.stopRun(agent, job.id),
    onSettled: () => qc.invalidateQueries({ queryKey: qk.jobs(agent) }),
  });
  const name = job.label || (job.kind === "sweep" ? "sweep" : fmtRunId(job.id).label);
  const sub = [fmtAt(job.at), job.kind];
  if (job.model) sub.push(shortModel(job.model));
  // auto-stop countdown, mirroring the RC-session TTL above (1.7)
  if (job.startedAt) {
    const stopAt = Date.parse(job.startedAt) / 1000 + (job.minutes || 480) * 60;
    sub.push(`auto-stop ${fmtLeft(stopAt)}`);
  }
  return (
    <SessionShell
      active
      kind="night"
      name={name}
      sub={sub.join(" · ")}
      executor={job.executor}
      onStop={() => stop.mutate()}
      stopping={stop.isPending}
    />
  );
}
