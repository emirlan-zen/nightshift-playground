import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  Blocks,
  Check,
  Clock3,
  FileCode2,
  GitBranch,
  Plus,
  ShieldAlert,
  Trash2,
} from "lucide-react";
import { api, type Flow, type FlowNode, type FlowStatus, type ScoreRow } from "@/lib/api";
import { qk, useFlowCatalog, useFlows, useRepos, useRunScores } from "@/lib/queries";
import { FlowCard } from "@/components/FlowCard";
import { ExecutorIcon } from "@/components/ExecutorIcon";
import { FlowGraph } from "@/features/graph/FlowGraph";
import { liveToDraft, parseMember, rolesOf, templateToDraft } from "@/features/graph/model";
import type { GraphDraft } from "@/features/graph/types";
import { NightHistory } from "@/features/automation/NightPage";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, Kicker } from "@/components/ui/card";
import { Input, Textarea } from "@/components/ui/field";
import { EmptyText, ErrorText, SectionHeader } from "@/components/SectionHeader";
import { HiddenAgentNotice } from "@/components/HiddenAgentNotice";
import { useAgentHidden } from "@/lib/visibility";
import { cn } from "@/lib/utils";
import { countdownAt, type Countdown, useNow } from "@/lib/useCountdown";

const flowTone: Record<FlowStatus, BadgeProps["tone"]> = {
  queued: "sched",
  running: "accent",
  complete: "ok",
  blocked: "danger",
  deadline: "review",
  stopped: "done",
};

const verdictTone = (v: string): BadgeProps["tone"] =>
  v === "complete" || v === "ok" ? "ok" : v === "blocked" ? "danger" : "review";

function localInput(d: Date) {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function FormField({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <div className="mb-1.5 flex items-baseline justify-between gap-3">
        <span className="font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
          {label}
        </span>
        {hint && <span className="font-mono text-[9.5px] text-dim">{hint}</span>}
      </div>
      {children}
    </label>
  );
}

function NewFlow() {
  const navigate = useNavigate();
  const { data: catalog } = useFlowCatalog();
  const { data: repos = [] } = useRepos();
  const [step, setStep] = useState(0);
  const [repoKey, setRepoKey] = useState("");
  const [createdAt] = useState(() => Date.now());
  const [template, setTemplate] = useState("full-delivery");
  const [goal, setGoal] = useState("");
  const [acceptance, setAcceptance] = useState("");
  const [guidance, setGuidance] = useState("");
  const [base, setBase] = useState("default");
  const [deadlineOn, setDeadlineOn] = useState(true);
  const [deadline, setDeadline] = useState(() => localInput(new Date(Date.now() + 12 * 3600_000)));
  // Instant by default since the 2026-07-12 dev-PR run proved it safe: the
  // launcher's auth flock stays the refresh guard, and fast launches remain
  // serialized one per tick. Profiles/sweeps keep the spaced launch gap.
  const [fastHandoff, setFastHandoff] = useState(true);
  const [maxConcurrent, setMaxConcurrent] = useState(1);
  const [customNodes, setCustomNodes] = useState<string[] | null>(null);
  const [selectedNode, setSelectedNode] = useState("refine");

  const selectedRepo = repos.find(
    (r) => `${r.agent}|${r.path}` === (repoKey || `${repos[0]?.agent}|${repos[0]?.path}`),
  );
  const selectedTemplate = catalog?.templates.find((t) => t.id === template);
  const nodes = customNodes ?? (selectedTemplate ? rolesOf(selectedTemplate) : []);
  const create = useMutation({
    mutationFn: () =>
      api.createFlow({
        agent: selectedRepo!.agent,
        repo: selectedRepo!.path,
        goal: goal.trim(),
        acceptance: acceptance
          .split("\n")
          .map((x) => x.trim())
          .filter(Boolean),
        guidance: guidance.trim() || undefined,
        fastHandoff: fastHandoff || undefined,
        maxConcurrent: maxConcurrent > 1 ? maxConcurrent : undefined,
        template,
        nodes: customNodes ?? undefined,
        deadline: deadlineOn ? new Date(deadline).toISOString() : undefined,
        base: base.trim() || "default",
      }),
    onSuccess: (f) => navigate(`/runs/${f.id}`),
  });

  const addNode = (role: string) => setCustomNodes([...(customNodes ?? nodes), role]);
  const removeNode = (i: number) =>
    setCustomNodes((customNodes ?? nodes).filter((_, x) => x !== i));
  const moveNode = (i: number, delta: number) => {
    const next = [...(customNodes ?? nodes)];
    const target = i + delta;
    if (target < 0 || target >= next.length) return;
    [next[i], next[target]] = [next[target], next[i]];
    setCustomNodes(next);
  };
  const selectedDef =
    catalog?.nodes.find((n) => n.id === selectedNode) ??
    catalog?.nodes.find((n) => n.id === nodes[0]);
  // The graph the run WILL get (mirrors the backend, automations.go): an
  // untouched template keeps its stages + needs-work routes; a customized
  // sequence mints one stage per node with no routes.
  const previewDraft = useMemo<GraphDraft>(() => {
    if (customNodes === null && selectedTemplate) return templateToDraft(selectedTemplate);
    return {
      template: { id: template, revision: selectedTemplate?.revision ?? "" },
      routes: [],
      nodes: (customNodes ?? []).map((role, i) => ({
        id: `h:${i}:0:${role}`,
        role,
        stage: i,
        order: 0,
        lane: "happy" as const,
      })),
    };
  }, [customNodes, selectedTemplate, template]);
  const selectedGraphId =
    previewDraft.nodes.find((n) => n.role === (selectedDef?.id ?? selectedNode))?.id ?? null;
  // A member slot ("attempt#2") is the same definition as its base role, so the
  // estimate must resolve it or a comparison automation prices as 0 minutes.
  const maxMinutes = nodes.reduce(
    (sum, member) =>
      sum + (catalog?.nodes.find((n) => n.id === parseMember(member).role)?.minutes ?? 0),
    0,
  );
  // A customized sequence drops the template's emitters (the backend mints one
  // stage per node with no routes), so emissions only apply to an untouched one.
  const emissionBudget =
    customNodes === null
      ? (selectedTemplate?.emitters ?? []).reduce((sum, e) => sum + Math.max(0, e.max) + 1, 0)
      : 0;
  const outcomeReady =
    !!selectedRepo &&
    !!goal.trim() &&
    (!deadlineOn || new Date(deadline).getTime() > createdAt + 10 * 60_000);
  const canCreate =
    outcomeReady && nodes.length > 0 && acceptance.split("\n").some((line) => line.trim());

  const steps = ["Outcome", "Automation", "Review"];

  return (
    <section className="mx-auto max-w-[1040px]">
      <div className="mb-5 flex items-center justify-between gap-3">
        <div>
          <Kicker>// new run</Kicker>
          <h2 className="mt-1.5 font-head text-[20px] font-bold tracking-tight">
            Launch an unattended run
          </h2>
        </div>
        <Button size="sm" onClick={() => navigate("/runs")}>
          <ArrowLeft className="size-3.5" /> Runs
        </Button>
      </div>

      <div className="mb-5 grid grid-cols-3 border border-line bg-surface">
        {steps.map((label, index) => (
          <button
            key={label}
            onClick={() => index < step && setStep(index)}
            className={cn(
              "min-h-12 border-r border-line px-2 text-left last:border-r-0 sm:px-4",
              index === step ? "bg-accent/8" : "",
            )}
          >
            <span
              className={cn(
                "mr-2 font-mono text-[9px]",
                index <= step ? "text-accent" : "text-dim",
              )}
            >
              0{index + 1}
            </span>
            <span
              className={cn(
                "font-head text-[12px] font-bold sm:text-[13px]",
                index === step ? "text-ink" : "text-mut",
              )}
            >
              {label}
            </span>
          </button>
        ))}
      </div>

      {step === 0 && (
        <div className="space-y-4">
          <Card className="space-y-4 p-4 sm:p-5">
            <FormField label="Goal" hint="task, issue, or ADR">
              <Textarea
                className="min-h-[122px] font-sans text-[15px]"
                placeholder="Refine the latest ADR until it is decided, buildable, and independently validated."
                value={goal}
                onChange={(e) => setGoal(e.target.value)}
              />
            </FormField>
            <div className="grid gap-4 sm:grid-cols-2">
              <FormField label="Repository">
                <select
                  data-testid="flow-repo"
                  value={repoKey || (repos[0] ? `${repos[0].agent}|${repos[0].path}` : "")}
                  onChange={(e) => setRepoKey(e.target.value)}
                  className="min-h-11 w-full border border-line2 bg-bg px-3 font-mono text-[12px] text-ink focus:border-accent focus:outline-none"
                >
                  {repos.map((r) => (
                    <option key={`${r.agent}|${r.path}`} value={`${r.agent}|${r.path}`}>
                      {r.agent} / {r.path}
                    </option>
                  ))}
                </select>
              </FormField>
              <FormField label="Finish by">
                <div className="flex gap-2">
                  <Input
                    disabled={!deadlineOn}
                    type="datetime-local"
                    value={deadline}
                    onChange={(e) => setDeadline(e.target.value)}
                  />
                  <button
                    type="button"
                    onClick={() => setDeadlineOn(!deadlineOn)}
                    className={cn(
                      "min-h-11 shrink-0 border px-3 font-mono text-[10px] uppercase tracking-wide",
                      deadlineOn ? "border-accent text-accent" : "border-line2 text-mut",
                    )}
                  >
                    {deadlineOn ? "Timed" : "No limit"}
                  </button>
                </div>
              </FormField>
            </div>
            {/* Not a FormField: a <label> wrapping buttons leaks the caption into
                their accessible names. */}
            <div>
              <div className="mb-1.5 flex items-baseline justify-between gap-3">
                <span className="font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                  Node handoff
                </span>
                <span className="font-mono text-[9.5px] text-dim">
                  how fast a node starts after its upstream delivers
                </span>
              </div>
              <div className="grid grid-cols-2 gap-2">
                {(
                  [
                    [false, "Spaced", "waits the 3-min box-wide launch gap"],
                    [true, "Instant", "starts on the next tick, skips the gap"],
                  ] as const
                ).map(([value, label, hint]) => (
                  <button
                    key={label}
                    type="button"
                    onClick={() => setFastHandoff(value)}
                    className={cn(
                      "min-h-11 border px-3 text-left",
                      fastHandoff === value ? "border-accent" : "border-line2",
                    )}
                  >
                    <span
                      className={cn(
                        "font-mono text-[10px] font-bold uppercase tracking-wide",
                        fastHandoff === value ? "text-accent" : "text-mut",
                      )}
                    >
                      {label}
                    </span>
                    <span className="ml-2 text-[10.5px] text-dim">{hint}</span>
                  </button>
                ))}
              </div>
            </div>
            <div>
              <div className="mb-1.5 flex items-baseline justify-between gap-3">
                <span className="font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                  Concurrency
                </span>
                <span className="font-mono text-[9.5px] text-dim">
                  sessions working at once — 1 serializes even fanned-out stages
                </span>
              </div>
              <div className="grid grid-cols-4 gap-2">
                {[1, 2, 3, 4].map((n) => (
                  <button
                    key={n}
                    type="button"
                    onClick={() => setMaxConcurrent(n)}
                    className={cn(
                      "min-h-11 border px-3 text-left",
                      maxConcurrent === n ? "border-accent" : "border-line2",
                    )}
                  >
                    <span
                      className={cn(
                        "font-mono text-[10px] font-bold uppercase tracking-wide",
                        maxConcurrent === n ? "text-accent" : "text-mut",
                      )}
                    >
                      {n === 1 ? "1 · serial" : `≤${n}`}
                    </span>
                  </button>
                ))}
              </div>
            </div>
          </Card>
          <Card className="p-4 sm:p-5">
            <FormField label="Acceptance criteria" hint="one measurable criterion per line">
              <Textarea
                className="min-h-[130px] font-sans"
                placeholder={
                  "The implementation path is test-green\nA realistic preview proves the user path\nThe PR is ready for independent review"
                }
                value={acceptance}
                onChange={(e) => setAcceptance(e.target.value)}
              />
            </FormField>
          </Card>
          <Button
            variant="accent"
            size="block"
            disabled={!outcomeReady || !acceptance.split("\n").some((line) => line.trim())}
            onClick={() => setStep(1)}
          >
            Choose automation <ArrowRight className="size-4" />
          </Button>
        </div>
      )}

      {step === 1 && (
        // The base template matters: an implicit auto track would size to the
        // graph preview's intrinsic width and overflow phones (the P0-1 class).
        <div className="grid grid-cols-[minmax(0,1fr)] gap-5 lg:grid-cols-[minmax(0,1fr)_340px]">
          <div className="space-y-4">
            <div>
              <SectionHeader className="mb-2" title="Template" meta="reusable session roles" />
              <div className="grid gap-2 sm:grid-cols-2">
                {catalog?.templates.map((t) => (
                  <button
                    key={t.id}
                    type="button"
                    onClick={() => {
                      setTemplate(t.id);
                      setCustomNodes(null);
                      setSelectedNode(rolesOf(t)[0] ?? "");
                    }}
                    className={cn(
                      // min-w-0: the truncated node-chain line is nowrap, so
                      // without it the card's min-content (full chain width)
                      // sizes the grid track and overflows phones (P0-1 class).
                      "min-w-0 border p-3.5 text-left transition-colors",
                      template === t.id && customNodes === null
                        ? "border-accent bg-accent/5"
                        : "border-line bg-surface hover:border-line2",
                    )}
                  >
                    <div className="flex items-center gap-2">
                      <span className="font-head text-[14px] font-bold">{t.name}</span>
                      {t.customized && <Badge tone="accent">custom</Badge>}
                    </div>
                    <div className="mt-1 text-[11.5px] leading-relaxed text-mut">
                      {t.description}
                    </div>
                    <div className="mt-2 truncate font-mono text-[9.5px] uppercase tracking-wide text-dim">
                      {rolesOf(t).join(" → ")}
                    </div>
                  </button>
                ))}
              </div>
            </div>
            <div>
              <SectionHeader
                className="mb-2"
                title="Shape"
                meta={customNodes === null ? "stages + needs-work routes" : "custom · sequential"}
              />
              <FlowGraph
                draft={previewDraft}
                catalog={catalog?.nodes ?? []}
                mode="template-readonly"
                selected={selectedGraphId}
                onSelect={(id) => {
                  const role = id.split(":")[3];
                  if (role) setSelectedNode(role);
                }}
                className="compact"
              />
            </div>
            <div>
              <SectionHeader
                className="mb-2"
                title="Sequence"
                meta={`${nodes.length} fresh sessions`}
              />
              <div className="space-y-2">
                {nodes.map((role, i) => {
                  const def = catalog?.nodes.find((n) => n.id === role);
                  return (
                    <Card
                      key={`${role}-${i}`}
                      className={cn(
                        "flex items-center gap-3 p-3",
                        selectedDef?.id === role && "border-accent/60",
                      )}
                    >
                      <span className="grid size-8 shrink-0 place-items-center border border-line2 font-mono text-[10px] text-mut">
                        {i + 1}
                      </span>
                      <button
                        className="min-w-0 flex-1 text-left"
                        onClick={() => setSelectedNode(role)}
                      >
                        <span className="block text-[13.5px] font-semibold">
                          {def?.name ?? role}
                        </span>
                        <span className="mt-0.5 block truncate text-[10.5px] text-dim">
                          {def?.description}
                        </span>
                      </button>
                      <div className="flex gap-1">
                        <button
                          aria-label={`Move ${role} up`}
                          className="tap-control"
                          disabled={i === 0}
                          onClick={() => moveNode(i, -1)}
                        >
                          <ArrowUp className="size-3.5" />
                        </button>
                        <button
                          aria-label={`Move ${role} down`}
                          className="tap-control"
                          disabled={i === nodes.length - 1}
                          onClick={() => moveNode(i, 1)}
                        >
                          <ArrowDown className="size-3.5" />
                        </button>
                        <button
                          aria-label={`Remove ${role}`}
                          className="tap-control hover:border-danger hover:text-danger"
                          onClick={() => removeNode(i)}
                        >
                          <Trash2 className="size-3.5" />
                        </button>
                      </div>
                    </Card>
                  );
                })}
              </div>
              <select
                aria-label="Add node"
                value=""
                onChange={(e) => e.target.value && addNode(e.target.value)}
                className="mt-2 min-h-11 w-full border border-dashed border-line2 bg-bg px-3 font-mono text-[10px] uppercase text-mut"
              >
                <option value="">+ Add node</option>
                {catalog?.nodes.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.name}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <div>
            {selectedDef ? (
              <Card className="sticky top-16 p-4">
                <div className="flex items-center justify-between gap-2">
                  <div className="font-head text-[16px] font-bold">{selectedDef.name}</div>
                  <Badge tone={selectedDef.promptSource === "custom" ? "accent" : "muted"}>
                    {selectedDef.promptSource}
                  </Badge>
                </div>
                <p className="mt-2 text-[12px] leading-relaxed text-mut">
                  {selectedDef.description}
                </p>
                <div className="mt-3 flex items-center gap-2 font-mono text-[9.5px] uppercase text-dim">
                  <span>{selectedDef.effort}</span>
                  <span>·</span>
                  <span>{selectedDef.minutes}m</span>
                  <span>·</span>
                  <ExecutorIcon
                    executor={selectedDef.executor}
                    className={cn(
                      "size-3",
                      selectedDef.executor === "codex" ? "text-ink" : "text-mut",
                    )}
                  />
                  <span>{selectedDef.executor === "codex" ? "codex" : "claude"}</span>
                  <span>·</span>
                  <span>{selectedDef.promptRevision}</span>
                </div>
                <div className="mt-4 flex items-center gap-2 border-b border-line pb-2 font-mono text-[10px] font-bold uppercase text-mut">
                  <FileCode2 className="size-3.5" /> Exact node prompt
                </div>
                <pre className="mt-3 max-h-[280px] overflow-auto whitespace-pre-wrap font-mono text-[10.5px] leading-relaxed text-[#c4c8d0]">
                  {selectedDef.prompt}
                </pre>
              </Card>
            ) : null}
          </div>
          <div className="flex gap-2 lg:col-span-2">
            <Button size="md" onClick={() => setStep(0)}>
              <ArrowLeft className="size-4" /> Outcome
            </Button>
            <Button
              variant="accent"
              size="md"
              className="flex-1"
              disabled={!nodes.length}
              onClick={() => setStep(2)}
            >
              Review run <ArrowRight className="size-4" />
            </Button>
          </div>
        </div>
      )}

      {step === 2 && (
        <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_340px]">
          <div className="space-y-4">
            <Card className="p-4 sm:p-5">
              <div className="font-mono text-[10px] font-bold uppercase text-mut">Outcome</div>
              <div className="mt-2 text-[16px] font-semibold leading-relaxed">{goal}</div>
              <div className="mt-3 font-mono text-[10px] uppercase text-dim">
                {selectedRepo?.agent} / {selectedRepo?.path} ·{" "}
                {deadlineOn
                  ? `finish ${new Date(deadline).toLocaleString()}`
                  : "no absolute deadline"}
              </div>
            </Card>
            <Card className="p-4 sm:p-5">
              <FormField label="Operator guidance" hint="applies to every remaining node">
                <Textarea
                  className="min-h-[120px] font-sans"
                  placeholder="Optional technical constraints or product preferences. Repository guidance is loaded automatically."
                  value={guidance}
                  onChange={(e) => setGuidance(e.target.value)}
                />
              </FormField>
              <div className="mt-4">
                <FormField label="Base ref">
                  <Input value={base} onChange={(e) => setBase(e.target.value)} />
                </FormField>
              </div>
            </Card>
            <Card className="p-4">
              <div className="mb-3 font-mono text-[10px] font-bold uppercase text-mut">
                Effective instruction stack
              </div>
              {[
                "Global contract",
                `${selectedRepo?.agent} repository guidance`,
                `${nodes.length} pinned node prompt revisions`,
                "Goal + acceptance criteria",
                "Upstream reports at each handoff",
              ].map((layer, i) => (
                <div
                  key={layer}
                  className="flex gap-3 border-t border-line/60 py-2.5 first:border-0"
                >
                  <span className="font-mono text-[9px] text-dim">0{i + 1}</span>
                  <span className="text-[12px]">{layer}</span>
                </div>
              ))}
            </Card>
          </div>
          <div className="space-y-4">
            <Card className="p-4">
              <div className="mb-3 font-mono text-[10px] font-bold uppercase text-mut">
                Run envelope
              </div>
              <div className="space-y-2 text-[12px]">
                <div className="flex justify-between">
                  <span className="text-mut">Automation</span>
                  <b>{selectedTemplate?.name}</b>
                </div>
                <div className="flex justify-between">
                  <span className="text-mut">Fresh sessions</span>
                  <b>{nodes.length}</b>
                </div>
                {/* An automation with emitters can decide it needs more sessions
                    than its stages list, so the envelope prices the worst case
                    before the operator commits (ADR-0023). */}
                {emissionBudget > 0 && (
                  <div className="flex justify-between" data-testid="envelope-emissions">
                    <span className="text-mut">Nodes it may add</span>
                    <b>up to {emissionBudget}</b>
                  </div>
                )}
                <div className="flex justify-between">
                  <span className="text-mut">Worst-case runtime</span>
                  <b>
                    {Math.floor(maxMinutes / 60)}h {maxMinutes % 60}m
                  </b>
                </div>
                <div className="flex justify-between">
                  <span className="text-mut">Worktree</span>
                  <b>isolated</b>
                </div>
                <div className="flex justify-between">
                  <span className="text-mut">Node handoff</span>
                  <b>{fastHandoff ? "instant" : "spaced"}</b>
                </div>
                <div className="flex justify-between">
                  <span className="text-mut">Concurrency</span>
                  <b>{maxConcurrent === 1 ? "serialized" : `≤${maxConcurrent} sessions`}</b>
                </div>
                <div className="flex justify-between">
                  <span className="text-mut">Acceptance loop</span>
                  <b>{nodes.includes("validate") ? "enabled" : "none"}</b>
                </div>
              </div>
              <div className="mt-4 flex flex-wrap gap-1.5">
                {nodes.map((role, i) => (
                  <span
                    key={`${role}-${i}`}
                    className="border border-line2 px-2 py-1 font-mono text-[9px] uppercase text-mut"
                  >
                    {i + 1}.{" "}
                    {catalog?.nodes.find((node) => node.id === parseMember(role).role)?.name ??
                      role}
                  </span>
                ))}
              </div>
            </Card>
            <Button
              variant="accent"
              size="block"
              disabled={!canCreate || create.isPending}
              onClick={() => create.mutate()}
            >
              {create.isPending ? "Preparing isolated worktree…" : "Start unattended run"}
            </Button>
            <Button size="block" onClick={() => setStep(1)}>
              <ArrowLeft className="size-4" /> Edit automation
            </Button>
            <ErrorText>{create.error instanceof Error ? create.error.message : null}</ErrorText>
            <p className="text-center font-mono text-[9.5px] leading-relaxed text-dim">
              The run pins this template and prompt revision set. Later edits cannot mutate work in
              flight.
            </p>
          </div>
        </div>
      )}
    </section>
  );
}

function CountdownChip({ countdown, label }: { countdown: Countdown | null; label: string }) {
  if (!countdown) return null;

  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 border px-1.5 py-0.5 font-mono text-[9px] uppercase tabular-nums",
        countdown.done
          ? "border-danger/50 bg-danger/5 text-danger"
          : "border-accent/50 bg-accent/5 text-accent-hi",
      )}
      aria-label={`${label}: ${countdown.done ? "time up" : `${countdown.label} remaining`}`}
    >
      <Clock3 className="size-3" aria-hidden="true" />
      {label} · {countdown.done ? "time up" : countdown.label}
    </span>
  );
}

/** One judge score, as shown on a node and in the run's score card. `unknown`
 * stays visibly unknown — a judge that could not tell must not read as a zero. */
function ScoreLine({ row, showSubject = false }: { row: ScoreRow; showSubject?: boolean }) {
  return (
    <div className="border-b border-line/60 py-2 last:border-0">
      <div className="flex items-baseline justify-between gap-2">
        <span className="font-mono text-[9.5px] uppercase text-dim">
          {showSubject ? `${row.subject} · ${row.dimension}` : row.dimension}
        </span>
        <span className="tabular-nums text-[12px]">
          {row.score === null ? (
            <span className="text-mut">unknown</span>
          ) : (
            <>
              <b>{row.score}</b>
              <span className="text-dim">/{row.max}</span>
            </>
          )}
        </span>
      </div>
      {row.rationale && (
        <p className="mt-0.5 text-[11.5px] leading-relaxed text-mut">{row.rationale}</p>
      )}
      <div className="mt-0.5 flex flex-wrap items-center gap-x-3 font-mono text-[9px] uppercase text-dim">
        {row.subjectExecutor && (
          <span className="flex items-center gap-1">
            <ExecutorIcon executor={row.subjectExecutor} className="size-3" />
            {row.subjectModel || row.subjectExecutor}
          </span>
        )}
        <span>judged by {row.judgeModel || row.judgeExecutor || row.judgeNode}</span>
      </div>
    </div>
  );
}

function nodeDeadline(node: FlowNode) {
  if (!node.startedAt) return null;
  const started = new Date(node.startedAt).getTime();
  if (!Number.isFinite(started)) return null;
  return new Date(started + node.minutes * 60_000);
}

function FlowDetail({ id }: { id: string }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: catalog } = useFlowCatalog();
  const flow = useQuery({
    queryKey: ["flow", id],
    queryFn: () => api.flow(id),
    refetchInterval: 7000,
  });
  const f = flow.data;
  const scores = useRunScores(id, f?.status === "running" || f?.status === "queued");
  const hidden = useAgentHidden(f?.agent);
  const [params, setParams] = useSearchParams();
  const [deadline, setDeadline] = useState("");
  const [guidance, setGuidance] = useState("");
  const selectedNodeId = params.get("node");
  const now = useNow(f?.status === "running" || f?.status === "queued");
  const runCountdown = f?.deadline ? countdownAt(f.deadline, now) : null;
  const nodeCountdown = (node: FlowNode) => {
    const target = nodeDeadline(node);
    return target ? countdownAt(target, now) : null;
  };
  const selectNode = (jobId: string) => {
    const next = new URLSearchParams(params);
    if (selectedNodeId === jobId) next.delete("node");
    else next.set("node", jobId);
    setParams(next, { replace: true });
  };
  const refresh = (x: Flow) => {
    qc.setQueryData(["flow", id], x);
    qc.invalidateQueries({ queryKey: qk.flows });
  };
  const updateDeadline = useMutation({
    mutationFn: () =>
      api.setFlowDeadline(id, deadline ? new Date(deadline).toISOString() : undefined),
    onSuccess: refresh,
  });
  const updateGuidance = useMutation({
    mutationFn: () => api.setFlowGuidance(id, guidance),
    onSuccess: (next) => {
      refresh(next);
      setGuidance("");
    },
  });
  const stop = useMutation({ mutationFn: () => api.stopFlow(id), onSuccess: refresh });
  const cleanup = useMutation({ mutationFn: () => api.cleanupFlow(id), onSuccess: refresh });
  if (flow.isLoading || !f) return <EmptyText>Loading run…</EmptyText>;
  if (hidden) return <HiddenAgentNotice />;

  const terminal = ["complete", "stopped", "deadline"].includes(f.status);
  // Fan-out summary shown above the run graph (ADR-0019): the widest parallel
  // stage and the concurrency cap that serializes it.
  const cap = f.maxConcurrent ?? 1;
  const stageWidths = f.stages?.map((s) => s.length) ?? [
    ...f.nodeViews
      .filter((n) => !n.round)
      .reduce(
        (m, n) => m.set(n.stage ?? 0, (m.get(n.stage ?? 0) ?? 0) + 1),
        new Map<number, number>(),
      )
      .values(),
  ];
  const fanOut = stageWidths.length ? Math.max(...stageWidths) : 1;
  const graphMeta =
    fanOut > 1
      ? `${fanOut} fanned out · ${cap > 1 ? `≤${cap} at once` : "one at a time"}`
      : cap > 1
        ? `runs up to ${cap} sessions at once`
        : "sessions execute one at a time";
  const selectedNode = f.nodeViews.find((node) => node.jobId === selectedNodeId);
  const scoreRows = scores.data?.rows ?? [];
  // A score names its subject by node id (a compared member) or by role, so a
  // node shows the scores about it either way.
  const scoresFor = (node: FlowNode) =>
    scoreRows.filter((row) => row.subject === node.id || row.subject === node.role);
  const emitterName = (nodeId?: string) =>
    nodeId ? (f.nodeViews.find((n) => n.id === nodeId)?.name ?? nodeId) : "";
  // Live decoration on the shared graph: each running node shows its own
  // countdown, and the URL-selected node renders pressed/selected.
  const runDraft = liveToDraft(f);
  runDraft.nodes = runDraft.nodes.map((n) => {
    const nv = f.nodeViews.find((x) => x.id === n.id);
    const countdown = nv?.state === "running" ? nodeCountdown(nv) : null;
    // The graph is now the only per-node surface, so the parallel-member marker
    // (once a "∥ parallel" badge in the deleted Progress list) rides the node's
    // live sub-line alongside any countdown.
    const parts = [
      nv?.worktree ? "∥ parallel" : null,
      countdown ? (countdown.done ? "time up" : `${countdown.label} left`) : null,
    ].filter(Boolean);
    return parts.length ? { ...n, sub: parts.join(" · ") } : n;
  });
  return (
    <section className="mx-auto max-w-[1200px]">
      <div className="mb-4 flex items-start justify-between gap-3">
        <div>
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <Badge tone={flowTone[f.status]} dot={f.status === "running"}>
              {f.status}
            </Badge>
            {!terminal && <CountdownChip countdown={runCountdown} label="run" />}
            <span className="font-mono text-[10px] text-dim">
              {f.agent} / {f.repo}
            </span>
            {f.automationRevision && (
              <span className="font-mono text-[9px] uppercase text-dim">
                automation {f.automationRevision}
              </span>
            )}
            {f.fastHandoff && (
              <span className="border border-accent/40 px-1.5 py-0.5 font-mono text-[9px] uppercase text-accent">
                instant handoff
              </span>
            )}
            <span className="border border-line2 px-1.5 py-0.5 font-mono text-[9px] uppercase text-mut">
              {(f.maxConcurrent ?? 1) > 1 ? `≤${f.maxConcurrent} concurrent` : "serialized"}
            </span>
          </div>
          <h2 className="max-w-[30ch] font-head text-[24px] font-bold leading-tight tracking-tight">
            {f.goal}
          </h2>
        </div>
        <Button size="sm" onClick={() => navigate("/runs")}>
          <ArrowLeft className="size-3.5" /> Runs
        </Button>
      </div>

      {f.status === "blocked" && (
        <div className="mb-4 flex gap-3 border border-danger/60 bg-danger/5 p-4 text-[13px]">
          <ShieldAlert className="mt-0.5 size-4 shrink-0 text-danger" />
          <div>
            <b>Run needs attention.</b>
            <div className="mt-1 text-mut">
              {f.cleanupMessage ||
                "A node could not continue safely. Open its report for the exact decision or failure."}
            </div>
          </div>
        </div>
      )}

      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-w-0">
          <SectionHeader
            className="mb-2"
            title="Run graph"
            meta={f.round ? `remediation round ${f.round}` : undefined}
            action={
              f.template && f.template !== "custom" ? (
                <Button size="sm" onClick={() => navigate(`/automations/templates/${f.template}`)}>
                  <Blocks className="size-3.5" /> Edit automation
                </Button>
              ) : undefined
            }
          />
          <p className="mb-2 max-w-[62ch] text-[11px] leading-relaxed text-dim">
            A live view of this run ({graphMeta}) — tap a node for its state, prompt, report, and
            live session. The shape is fixed for a run; to change it, edit the automation.
          </p>
          <Card className="p-4">
            <FlowGraph
              draft={runDraft}
              catalog={catalog?.nodes ?? []}
              mode="run"
              selected={selectedNode?.id ?? null}
              onSelect={(nodeId) => {
                const nv = f.nodeViews.find((x) => x.id === nodeId);
                if (nv) selectNode(nv.jobId);
              }}
              detail={
                selectedNode ? (
                  <div data-testid="graph-node-overlay">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-head text-[14px] font-bold">{selectedNode.name}</span>
                      <Badge tone={selectedNode.state === "running" ? "accent" : "muted"}>
                        {selectedNode.state}
                      </Badge>
                      {selectedNode.verdict ? (
                        <Badge tone={verdictTone(selectedNode.verdict)}>
                          {selectedNode.verdict}
                        </Badge>
                      ) : null}
                      {selectedNode.state === "running" ? (
                        <CountdownChip countdown={nodeCountdown(selectedNode)} label="stage" />
                      ) : null}
                    </div>
                    <p className="mt-1 text-[11px] leading-relaxed text-mut">
                      {selectedNode.description}
                    </p>
                    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[9px] uppercase text-dim">
                      <span>{selectedNode.executor === "codex" ? "codex" : "claude"}</span>
                      <span>{selectedNode.effort}</span>
                      <span>{selectedNode.minutes}m window</span>
                    </div>
                    {selectedNode.reportId && (
                      <Button
                        size="sm"
                        className="mt-2"
                        onClick={() =>
                          navigate(
                            `/night/${f.agent}/${encodeURIComponent(selectedNode.reportId!)}`,
                          )
                        }
                      >
                        Report
                      </Button>
                    )}
                  </div>
                ) : undefined
              }
            />
            {selectedNode ? (
              <div data-testid="graph-node-detail" className="mt-3 border-t border-line pt-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-head text-[14px] font-bold">{selectedNode.name}</span>
                      <Badge tone={selectedNode.state === "running" ? "accent" : "muted"}>
                        {selectedNode.state}
                      </Badge>
                      {selectedNode.verdict ? (
                        <Badge tone={verdictTone(selectedNode.verdict)}>
                          {selectedNode.verdict}
                        </Badge>
                      ) : null}
                      {selectedNode.round ? (
                        <Badge tone="muted">retry {selectedNode.round}</Badge>
                      ) : null}
                      {selectedNode.worktree ? <Badge tone="accent">∥ parallel</Badge> : null}
                      {/* Provenance: this node did not come from the automation's
                          stages — an upstream node decided the run needed it. */}
                      {selectedNode.emittedBy ? (
                        <Badge tone="accent">
                          emitted by {emitterName(selectedNode.emittedBy)}
                        </Badge>
                      ) : null}
                      {selectedNode.state === "running" ? (
                        <CountdownChip countdown={nodeCountdown(selectedNode)} label="stage" />
                      ) : null}
                    </div>
                    <p className="mt-1 text-[11.5px] leading-relaxed text-mut">
                      {selectedNode.description}
                    </p>
                  </div>
                  <button
                    className="shrink-0 font-mono text-[9px] uppercase tracking-wider text-dim hover:text-ink"
                    onClick={() => selectNode(selectedNode.jobId)}
                  >
                    Close
                  </button>
                </div>
                <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[9px] uppercase text-dim">
                  <span className="flex items-center gap-1.5">
                    <ExecutorIcon
                      executor={selectedNode.executor}
                      className={cn(
                        "size-3",
                        selectedNode.executor === "codex" ? "text-ink" : "text-mut",
                      )}
                    />
                    {selectedNode.executor === "codex" ? "codex" : "claude"}
                  </span>
                  {selectedNode.model && <span className="normal-case">{selectedNode.model}</span>}
                  <span>{selectedNode.effort}</span>
                  <span>{selectedNode.minutes}m window</span>
                  <span>prompt {selectedNode.promptRevision}</span>
                </div>
                <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[9px] uppercase text-dim">
                  <span className="break-all">job {selectedNode.jobId}</span>
                  <span>
                    {selectedNode.startedAt
                      ? `started ${new Date(selectedNode.startedAt).toLocaleString()}`
                      : "not started"}
                  </span>
                </div>
                <div className="mt-2 text-[11.5px]">
                  <span className="font-mono text-[9px] uppercase text-dim">Produces</span>
                  <span className="ml-2">{selectedNode.output}</span>
                </div>
                {scoresFor(selectedNode).length > 0 && (
                  <div className="mt-3 border-t border-line pt-2" data-testid="node-scores">
                    <div className="mb-1 font-mono text-[9px] uppercase tracking-wider text-dim">
                      Judged
                    </div>
                    {scoresFor(selectedNode).map((row) => (
                      <ScoreLine key={`${row.judgeNode}-${row.dimension}`} row={row} />
                    ))}
                  </div>
                )}
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    onClick={() => navigate(`/automations/nodes/${selectedNode.role}`)}
                  >
                    <FileCode2 className="size-3.5" /> Inspect prompt
                  </Button>
                  {selectedNode.reportId && (
                    <Button
                      size="sm"
                      onClick={() =>
                        navigate(`/night/${f.agent}/${encodeURIComponent(selectedNode.reportId!)}`)
                      }
                    >
                      Report
                    </Button>
                  )}
                  {selectedNode.state === "running" && (
                    <Button size="sm" variant="accent" onClick={() => navigate("/sessions")}>
                      Open live session
                    </Button>
                  )}
                </div>
              </div>
            ) : (
              <p className="mt-3 border-t border-line pt-3 font-mono text-[10px] uppercase tracking-wider text-dim">
                Tap a node for its countdown, prompt revision, report, and live session.
              </p>
            )}
          </Card>
        </div>
        <div className="space-y-5">
          {scoreRows.length > 0 && (
            <div data-testid="run-scores">
              <SectionHeader
                className="mb-2"
                title="Judge scores"
                meta={`${new Set(scoreRows.map((r) => r.subject)).size} judged`}
              />
              <Card className="px-3 py-1">
                {[...new Set(scoreRows.map((r) => r.subject))].map((subject) => (
                  <div key={subject} className="border-b border-line/60 py-2 last:border-0">
                    <div className="font-mono text-[9.5px] uppercase text-mut">{subject}</div>
                    {scoreRows
                      .filter((r) => r.subject === subject)
                      .map((row) => (
                        <ScoreLine key={`${row.judgeNode}-${row.dimension}`} row={row} />
                      ))}
                  </div>
                ))}
              </Card>
              <p className="mt-1.5 text-[11px] leading-relaxed text-dim">
                Each score records who was judged and who judged, so a self-preferring judge is
                auditable rather than invisible.
              </p>
            </div>
          )}
          <div>
            <SectionHeader className="mb-2" title="Acceptance" />
            <Card className="px-4 py-2">
              {f.acceptance?.length ? (
                f.acceptance.map((a) => (
                  <div
                    key={a}
                    className="flex gap-2 border-b border-line/60 py-2.5 text-[12px] last:border-0"
                  >
                    <Check className="mt-0.5 size-3.5 shrink-0 text-dim" />
                    <span>{a}</span>
                  </div>
                ))
              ) : (
                <EmptyText>No explicit criteria.</EmptyText>
              )}
            </Card>
          </div>
          <div>
            <SectionHeader className="mb-2" title="Finish by" />
            <Card className="p-3">
              <div className="mb-2 flex items-center gap-2 font-mono text-[10.5px] text-mut">
                <Clock3 className="size-3.5" />
                {f.deadline ? new Date(f.deadline).toLocaleString() : "No run deadline"}
              </div>
              <div className="flex gap-2">
                <Input
                  type="datetime-local"
                  value={deadline}
                  placeholder={f.deadline ? localInput(new Date(f.deadline)) : ""}
                  onChange={(e) => setDeadline(e.target.value)}
                />
                <Button
                  size="sm"
                  disabled={updateDeadline.isPending}
                  onClick={() => updateDeadline.mutate()}
                >
                  {deadline ? "Update" : "Clear"}
                </Button>
              </div>
            </Card>
          </div>
          <div>
            <SectionHeader className="mb-2" title="Guidance" meta="remaining nodes" />
            <Card className="p-3">
              {f.guidance && (
                <div className="mb-2 border-l-2 border-accent pl-3 text-[11.5px] leading-relaxed text-mut">
                  {f.guidance}
                </div>
              )}
              <Textarea
                className="min-h-[92px] font-sans text-[12px]"
                value={guidance}
                onChange={(e) => setGuidance(e.target.value)}
                placeholder="Add or replace operator guidance for every node that has not started."
              />
              <Button
                className="mt-2"
                size="sm"
                disabled={updateGuidance.isPending}
                onClick={() => updateGuidance.mutate()}
              >
                {guidance ? "Update guidance" : "Clear guidance"}
              </Button>
            </Card>
          </div>
        </div>
      </div>

      <details className="mt-6 border border-line bg-surface">
        <summary className="cursor-pointer px-4 py-3 font-mono text-[10px] uppercase tracking-wider text-mut">
          Technical details
        </summary>
        <div className="grid gap-2 border-t border-line p-4 font-mono text-[10.5px] text-mut">
          <div className="flex gap-2">
            <GitBranch className="size-3.5" />
            {f.branch || "branch pending"}
          </div>
          <div className="break-all">worktree: {f.worktree || f.worktreeState}</div>
          <div>
            base: {f.base} · batch: {f.batch}
          </div>
          <div>
            template: {f.template} · automation revision: {f.automationRevision || "legacy"}
          </div>
          {f.cleanupMessage && <div>cleanup: {f.cleanupMessage}</div>}
        </div>
      </details>

      <div className="mt-5 flex flex-wrap gap-2">
        {!terminal && f.status !== "blocked" && (
          <Button variant="danger" disabled={stop.isPending} onClick={() => stop.mutate()}>
            Stop remaining work
          </Button>
        )}
        {terminal && f.worktreeState !== "cleaned" && (
          <Button disabled={cleanup.isPending} onClick={() => cleanup.mutate()}>
            <Trash2 className="size-3.5" /> Clean worktree
          </Button>
        )}
      </div>
      <ErrorText>
        {
          [updateDeadline.error, updateGuidance.error, stop.error, cleanup.error].find(
            (e) => e instanceof Error,
          )?.message
        }
      </ErrorText>
    </section>
  );
}

function FlowList() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const { data: flows = [], isLoading } = useFlows();
  const attention = params.get("filter") === "attention";
  const visible = attention
    ? flows.filter((f) => f.status === "blocked" || f.status === "deadline")
    : flows;
  const active = visible.filter((f) => f.status === "running" || f.status === "queued");
  const history = visible.filter((f) => f.status !== "running" && f.status !== "queued");
  return (
    <section>
      <div className="mb-4 flex items-center justify-between gap-3">
        <Kicker>// runs</Kicker>
        <Button variant="accent" size="md" onClick={() => navigate("/runs/new")}>
          <Plus className="size-4" /> New run
        </Button>
      </div>
      {isLoading ? (
        <EmptyText>Loading runs…</EmptyText>
      ) : (
        <>
          <SectionHeader
            className="mb-3"
            title={attention ? "Needs attention" : "Active"}
            meta={`${active.length}`}
          />
          <div className="grid gap-3 lg:grid-cols-2">
            {active.length ? (
              active.map((f) => (
                <FlowCard key={f.id} flow={f} onOpen={() => navigate(`/runs/${f.id}`)} />
              ))
            ) : (
              <Card className="col-span-full p-4">
                <EmptyText>{attention ? "No blocked runs." : "No active runs."}</EmptyText>
              </Card>
            )}
          </div>
          {history.length > 0 && (
            <>
              <SectionHeader className="mb-3 mt-8" title="Recent runs" meta={`${history.length}`} />
              <div className="grid gap-2 lg:grid-cols-2">
                {history.map((f) => (
                  <FlowCard compact key={f.id} flow={f} onOpen={() => navigate(`/runs/${f.id}`)} />
                ))}
              </div>
            </>
          )}
          {!attention && (
            <>
              <SectionHeader
                className="mb-3 mt-8"
                title="Night runs & schedules"
                meta="per agent · sweeps + reports"
              />
              <NightHistory />
            </>
          )}
        </>
      )}
    </section>
  );
}

export function Flows() {
  const { id } = useParams();
  if (id === "new") return <NewFlow />;
  if (id) return <FlowDetail id={id} />;
  return <FlowList />;
}
