import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { api, type ServerState } from "@/lib/api";
import { qk, useServers, useJobs } from "@/lib/queries";
import { Card } from "@/components/ui/card";
import { Kicker } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ClaudeBot } from "@/components/ClaudeBot";
import { RcSessionRow, NightSessionRow } from "@/components/SessionRow";
import { ExecutorIcon } from "@/components/ExecutorIcon";
import { EmptyText, ErrorText } from "@/components/SectionHeader";
import { cn } from "@/lib/utils";

const MAX_SESSIONS = 6; // mirrors RC_MAX_SESSIONS in nightshift-rc

function ServerCard({ s }: { s: ServerState }) {
  const qc = useQueryClient();
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState("");

  // Night-run sessions that are live right now — surfaced here for visibility;
  // scheduling still lives in the Night tab.
  const jobs = useJobs(s.company);
  const nightLive = (jobs.data ?? []).filter((j) => j.started && j.runState === "active");

  const rc = s.sessions ?? [];
  const up = s.active || nightLive.length > 0;
  const atCap = rc.length >= MAX_SESSIONS;

  const start = useMutation({
    mutationFn: (slot?: string) => api.sessionStart(s.company, slot),
    onSettled: () => qc.invalidateQueries({ queryKey: qk.status }),
    onSuccess: () => {
      setName("");
      setNaming(false);
    },
  });

  const anyLive = rc.length > 0 || nightLive.length > 0;

  return (
    <Card className={cn("flex flex-col p-[18px]", up && "border-ok/45 bg-bg2")}>
      <div className="flex items-center gap-2.5">
        <div
          data-testid={`server-card-title-${s.company}`}
          className="font-head text-[19px] font-bold tracking-tight"
        >
          {s.company}
        </div>
        {up && <ClaudeBot active size={24} className="ml-auto" />}
        <Badge tone={up ? "ok" : "muted"} dot className={up ? "" : "ml-auto"}>
          {up ? "running" : "stopped"}
        </Badge>
      </div>
      <div className="mt-1 font-mono text-[9px] uppercase tracking-wider text-dim">
        workspace / {s.company} · independent Claude endpoint
      </div>

      <div className="mt-3">
        {anyLive ? (
          <>
            {rc.map((sess) => (
              <RcSessionRow key={sess.instance} company={s.company} session={sess} />
            ))}
            {nightLive.map((job) => (
              <NightSessionRow key={job.id} agent={s.company} job={job} />
            ))}
          </>
        ) : (
          <EmptyText>No live sessions.</EmptyText>
        )}
      </div>

      {naming ? (
        <div className="mt-[14px] flex items-stretch gap-2">
          <input
            autoFocus
            placeholder="name (optional)"
            value={name}
            maxLength={21}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && start.mutate(name.trim() || undefined)}
            className="min-w-0 flex-1 border border-line2 bg-bg px-3 py-2 font-mono text-[13px] text-ink focus:border-accent focus:outline-none"
          />
          <Button
            variant="accent"
            size="sm"
            disabled={start.isPending}
            onClick={() => start.mutate(name.trim() || undefined)}
          >
            {start.isPending ? "Starting…" : "Start"}
          </Button>
        </div>
      ) : (
        <Button
          variant={anyLive ? "ghost" : "accent"}
          size="block"
          className="mt-[14px]"
          disabled={atCap || start.isPending}
          onClick={() => (anyLive ? setNaming(true) : start.mutate(undefined))}
        >
          <Plus className="size-3" />
          {atCap ? `Max ${MAX_SESSIONS} sessions` : anyLive ? "New session" : "Start session"}
        </Button>
      )}

      <ErrorText>{start.error instanceof Error ? start.error.message : null}</ErrorText>
    </Card>
  );
}

export function Servers() {
  const { data, error } = useServers(5000);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [quickAgent, setQuickAgent] = useState("");
  const [quickName, setQuickName] = useState("");
  const selectedAgent = quickAgent || data?.[0]?.company || "";
  const quickStart = useMutation({
    mutationFn: () => api.sessionStart(selectedAgent, quickName.trim() || undefined),
    onSuccess: () => {
      setQuickName("");
      qc.invalidateQueries({ queryKey: qk.status });
    },
  });
  const live = (data ?? [])
    .flatMap((server) => server.sessions ?? [])
    .filter((session) => session.active).length;
  return (
    <section>
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <Kicker>// remote sessions</Kicker>
          <h1 className="mt-2 font-head text-[27px] font-bold tracking-tight">
            Claude Code, ready when you are.
          </h1>
          <p className="mt-1 max-w-[68ch] text-[13px] leading-relaxed text-mut">
            Start independent remote-control endpoints by agent workspace. Autonomous run sessions
            appear here too, so every live process has one stop surface.
          </p>
        </div>
        <Badge tone={live ? "ok" : "muted"} dot={live > 0}>
          {live ? `${live} live` : "idle"}
        </Badge>
      </div>
      <Card className="mb-5 p-3.5 sm:p-4">
        <div className="mb-2 font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
          Quick start · 24h auto-stop
        </div>
        <div className="grid gap-2 sm:grid-cols-[180px_minmax(0,1fr)_auto]">
          <select
            aria-label="Session agent"
            value={selectedAgent}
            onChange={(e) => setQuickAgent(e.target.value)}
            className="min-h-11 border border-line2 bg-bg px-3 font-mono text-[11px] uppercase text-ink focus:border-accent"
          >
            {(data ?? []).map((server) => (
              <option key={server.company} value={server.company}>
                {server.company}
              </option>
            ))}
          </select>
          <input
            aria-label="Session name"
            placeholder="name / purpose (optional)"
            value={quickName}
            maxLength={21}
            onChange={(e) => setQuickName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && selectedAgent && quickStart.mutate()}
            className="min-h-11 min-w-0 border border-line2 bg-bg px-3 font-mono text-[12px] text-ink focus:border-accent"
          />
          <Button
            variant="accent"
            size="md"
            disabled={!selectedAgent || quickStart.isPending}
            onClick={() => quickStart.mutate()}
          >
            <Plus className="size-4" /> {quickStart.isPending ? "Starting…" : "Start session"}
          </Button>
        </div>
        <ErrorText>{quickStart.error instanceof Error ? quickStart.error.message : null}</ErrorText>
      </Card>
      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2 min-[960px]:grid-cols-3">
        {(data ?? []).map((s) => (
          <ServerCard key={s.company} s={s} />
        ))}
      </div>
      <ErrorText>{error instanceof Error ? error.message : null}</ErrorText>
      <div className="mt-[30px] max-w-[64ch] text-[11px] leading-relaxed text-dim">
        Each session is an independent remote-control endpoint. Open the Claude app and select the
        matching agent / slot name to attach. Sessions and night runs auto-stop 24h / 8h after
        start; stop any of them here. Up to {MAX_SESSIONS} sessions per agent.
      </div>
      <div className="mt-4 grid gap-2 border-t border-line pt-4 max-w-[64ch] text-[11px] leading-relaxed text-dim">
        <div className="flex items-start gap-2.5">
          <ExecutorIcon executor="claude" className="mt-0.5 size-4 text-mut" />
          <span>
            <b className="text-mut">Claude</b> sessions are interactive — start one here and attach
            in the Claude app.
          </span>
        </div>
        <div className="flex items-start gap-2.5">
          <ExecutorIcon executor="codex" className="mt-0.5 size-4 text-mut" />
          <span>
            <b className="text-mut">Codex</b> (OpenAI Codex CLI, ADR-0018) has no interactive attach
            — a Codex session is a report-only run. Queue one with the executor picker in{" "}
            <button
              onClick={() => navigate("/night")}
              data-testid="run-history-link"
              className="inline-flex min-h-[44px] items-center align-middle -my-3 text-accent underline underline-offset-2 hover:text-accent-hi"
            >
              Run history
            </button>{" "}
            or on a node's runtime tab.
          </span>
        </div>
      </div>
    </section>
  );
}
