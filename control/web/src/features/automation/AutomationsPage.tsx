import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  Blocks,
  Clock3,
  FileCode2,
  GitCompare,
  History,
  Plus,
  Redo2,
  RotateCcw,
  Save,
  Timer,
  Trash2,
  Undo2,
  Workflow,
} from "lucide-react";
import {
  api,
  type EmitterSpec,
  type FlowNodeDefinition,
  type FlowTemplate,
  type NodeRuntimeOverride,
  type PromptDocument,
  type RouteEdge,
  type ScoreGroup,
} from "@/lib/api";
import {
  qk,
  useAutomationScores,
  useFlowCatalog,
  useProfiles,
  usePromptDocument,
} from "@/lib/queries";
import { SchedulesSection } from "@/features/automation/PipelinePage";
import { FlowGraph } from "@/features/graph/FlowGraph";
import {
  EMITTER_MAX_WIDTH,
  RUN_SESSION_CAP,
  nextMemberSlot,
  parseMember,
  stagesOf,
  templateToDraft,
  validateDraft,
  validateEmitters,
  worstCaseSessions,
} from "@/features/graph/model";
import { decodeGraphState, encodeGraphState } from "@/features/graph/urlState";
import { CUSTOM_ICON_CHOICES, NodeIcon } from "@/features/graph/nodeIcons";
import { fmtAt } from "@/lib/format";
import { setNavGuard } from "@/lib/navguard";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, Kicker } from "@/components/ui/card";
import { Input, Textarea } from "@/components/ui/field";
import { EmptyText, ErrorText, SectionHeader } from "@/components/SectionHeader";
import { ExecutorIcon } from "@/components/ExecutorIcon";
import { cn } from "@/lib/utils";
import { ApiError } from "@/shared/api/http";
import type { GraphIssue } from "@/features/graph/types";

function NodeSequence({ template }: { template: FlowTemplate }) {
  const { data } = useFlowCatalog();
  const name = (member: string) => {
    const { role, slot } = parseMember(member);
    const label = data?.nodes.find((n) => n.id === role)?.name ?? role;
    return slot ? `${label} #${slot}` : label;
  };
  return (
    <div className="flex flex-wrap gap-1.5">
      {stagesOf(template).map((stage, index) => (
        <span
          key={`${stage.join("+")}-${index}`}
          className={cn(
            "border px-2 py-1 font-mono text-[9.5px] uppercase tracking-wide",
            stage.length > 1 ? "border-accent/50 bg-bg text-ink" : "border-line2 bg-bg text-mut",
          )}
        >
          {index + 1}. {stage.map(name).join(" ∥ ")}
        </span>
      ))}
      {/* An automation whose shape can grow at runtime reads differently from a
          fixed one, so the list says so before it is opened (ADR-0023). */}
      {(template.emitters ?? []).map((e) => (
        <span
          key={`emits-${e.node}`}
          className="border border-accent/50 bg-bg px-2 py-1 font-mono text-[9.5px] uppercase tracking-wide text-accent"
        >
          {e.node} emits ≤{e.max} → {e.fanIn}
        </span>
      ))}
      {(template.edges ?? []).length > 0 && (
        <span className="border border-dashed border-line2 px-2 py-1 font-mono text-[9.5px] uppercase tracking-wide text-dim">
          {(template.edges ?? []).length} edge{(template.edges ?? []).length > 1 ? "s" : ""}
        </span>
      )}
    </div>
  );
}

export function Automations() {
  const navigate = useNavigate();
  const { data: catalog } = useFlowCatalog();
  const { data: profileDoc } = useProfiles();
  const customized = catalog?.templates.filter((t) => t.customized).length ?? 0;
  const customPrompts = catalog?.nodes.filter((n) => n.promptSource === "custom").length ?? 0;

  return (
    <section>
      <div className="mb-4 flex items-center justify-between gap-3">
        <Kicker>// automation studio</Kicker>
        <Button variant="accent" size="md" onClick={() => navigate("/runs/new")}>
          <Plus className="size-4" /> New run
        </Button>
      </div>

      <div className="mb-6 grid grid-cols-1 gap-2 sm:grid-cols-3">
        <Card className="flex items-center gap-3 p-4">
          <Workflow className="size-4 text-accent" />
          <div>
            <div className="font-head text-[18px] font-bold">{catalog?.templates.length ?? 0}</div>
            <div className="font-mono text-[9.5px] uppercase text-dim">
              templates · {customized} customized
            </div>
          </div>
        </Card>
        <Card className="flex items-center gap-3 p-4">
          <FileCode2 className="size-4 text-review" />
          <div>
            <div className="font-head text-[18px] font-bold">{catalog?.nodes.length ?? 0}</div>
            <div className="font-mono text-[9.5px] uppercase text-dim">
              node prompts · {customPrompts} custom
            </div>
          </div>
        </Card>
        <Card className="flex items-center gap-3 p-4">
          <Clock3 className="size-4 text-sched" />
          <div>
            <div className="font-head text-[18px] font-bold">
              {profileDoc?.profiles.length ?? 0}
            </div>
            <div className="font-mono text-[9.5px] uppercase text-dim">recurring schedules</div>
          </div>
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-[minmax(0,1.25fr)_minmax(340px,.75fr)]">
        <div className="min-w-0">
          <SectionHeader
            className="mb-3"
            title="Run templates"
            meta="goal-oriented"
            action={
              <Button size="sm" onClick={() => navigate("/automations/templates/new")}>
                <Plus className="size-3" /> Template
              </Button>
            }
          />
          <div className="space-y-2">
            {(catalog?.templates ?? []).map((template) => (
              <button
                key={template.id}
                onClick={() => navigate(`/automations/templates/${template.id}`)}
                className="block w-full border border-line bg-surface p-4 text-left transition-colors hover:border-line2 hover:bg-raise"
              >
                <div className="flex items-start gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-head text-[15px] font-bold">{template.name}</span>
                      {template.builtin && <Badge tone="muted">built-in</Badge>}
                      {template.customized && <Badge tone="accent">customized</Badge>}
                      {template.schedule && (
                        <Badge tone="ok">
                          daily {template.schedule.time} · {template.schedule.agent}
                        </Badge>
                      )}
                    </div>
                    <p className="mb-3 mt-1 text-[12px] leading-relaxed text-mut">
                      {template.description}
                    </p>
                    <NodeSequence template={template} />
                  </div>
                  <ArrowRight className="mt-1 size-4 shrink-0 text-dim" />
                </div>
              </button>
            ))}
          </div>
        </div>

        <div className="min-w-0 space-y-6">
          <div>
            <SectionHeader
              className="mb-3"
              title="Node library"
              action={
                <Button size="sm" onClick={() => navigate("/automations/nodes")}>
                  All nodes
                </Button>
              }
            />
            <Card className="px-4 py-1">
              {(catalog?.nodes ?? []).map((node) => (
                <button
                  key={node.id}
                  onClick={() => navigate(`/automations/nodes/${node.id}`)}
                  className="flex min-h-[58px] w-full items-center gap-3 border-b border-line/60 py-3 text-left last:border-0"
                >
                  <NodeIcon
                    name={node.icon}
                    className={cn(
                      "size-5 shrink-0",
                      node.promptSource === "custom" ? "text-accent" : "text-mut",
                    )}
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block text-[13px] font-semibold">{node.name}</span>
                    <span className="mt-0.5 block truncate text-[10.5px] text-dim">
                      {node.description}
                    </span>
                  </span>
                  <span className="font-mono text-[9px] uppercase text-dim">
                    {node.promptRevision}
                  </span>
                </button>
              ))}
            </Card>
          </div>
          <SchedulesSection />
        </div>
      </div>
    </section>
  );
}

/** ADR-0023: how well this automation's nodes are scoring, by prompt revision.
 * Plain numbers and one arrow — the question is "did the last prompt edit make
 * this node better", which a chart would not answer any faster. */
function ScoresSection({ automation }: { automation: string }) {
  const { data, isLoading, error } = useAutomationScores(automation);
  const groups = data?.groups ?? [];
  // Rows are (node, dimension, engine) — the runtime belongs in the key, or the
  // arrow would compare claude's revision with codex's. Revisions run newest-first
  // inside a row, so the arrow compares a revision with the one it replaced.
  const keyOf = (g: ScoreGroup) =>
    [g.role, g.dimension, g.executor ?? "", g.model ?? ""].join("\u0000");
  const keys = [...new Set(groups.map(keyOf))];
  const fmt = (n: number | null) => (n === null ? "—" : n.toFixed(1));
  return (
    <div data-testid="automation-scores">
      <SectionHeader
        className="mb-3"
        title="Judge scores"
        meta={groups.length ? `${keys.length} scored dimensions` : "no scores yet"}
      />
      {error instanceof Error && <ErrorText>{error.message}</ErrorText>}
      {!groups.length ? (
        <Card className="p-4">
          <EmptyText>
            {isLoading
              ? "Loading scores…"
              : "Nothing has judged this automation yet. Add a judge node and its scores land here, by prompt revision."}
          </EmptyText>
        </Card>
      ) : (
        <Card className="p-0">
          <div className="overflow-x-auto">
            <table className="w-full min-w-[420px] text-left text-[12px]">
              <thead>
                <tr className="border-b border-line font-mono text-[9px] uppercase tracking-wider text-dim">
                  <th className="px-3 py-2">Node</th>
                  <th className="px-3 py-2">Dimension</th>
                  <th className="px-3 py-2">Prompt</th>
                  <th className="px-3 py-2 text-right">Avg</th>
                  <th className="px-3 py-2 text-right">n</th>
                </tr>
              </thead>
              <tbody>
                {keys.map((key) => {
                  const rows = groups.filter((g) => keyOf(g) === key);
                  return rows.map((g, index) => {
                    const older = rows[index + 1];
                    // The delta is in PERCENTAGE POINTS, not raw score: a judge
                    // that moved from /5 to /10 between revisions changed the
                    // scale, not the quality, and subtracting the raw averages
                    // reported that as a regression.
                    const delta =
                      g.pct !== null && older?.pct !== undefined && older?.pct !== null
                        ? g.pct - older.pct
                        : null;
                    return (
                      <tr
                        key={`${key}-${g.promptRev}`}
                        className="border-b border-line/60 last:border-0 align-top"
                      >
                        <td className="px-3 py-2">
                          {index === 0 ? g.role : ""}
                          {index === 0 && g.executor && (
                            // The engine that produced the judged work — without it
                            // a comparison table is two anonymous rows.
                            <span className="ml-1.5 font-mono text-[9px] uppercase text-dim">
                              {g.model || g.executor}
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-2">{index === 0 ? g.dimension : ""}</td>
                        <td className="px-3 py-2 font-mono text-[10px] text-dim">{g.promptRev}</td>
                        <td className="px-3 py-2 text-right tabular-nums">
                          <b>{fmt(g.avg)}</b>
                          <span className="text-dim">/{g.max}</span>
                          {delta !== null && Math.abs(delta) >= 1 && (
                            <span
                              className={cn("ml-1.5", delta > 0 ? "text-ok" : "text-danger")}
                              aria-label={`${delta > 0 ? "up" : "down"} ${Math.round(Math.abs(delta))} points versus ${older?.promptRev}`}
                            >
                              {delta > 0 ? "▲" : "▼"}
                              {Math.round(Math.abs(delta))}
                            </span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right tabular-nums text-mut">{g.n}</td>
                      </tr>
                    );
                  });
                })}
              </tbody>
            </table>
          </div>
          {groups.some((g) => g.latestRationales?.length) && (
            <div className="border-t border-line px-3 py-2">
              <div className="mb-1 font-mono text-[9px] uppercase tracking-wider text-dim">
                Latest rationales
              </div>
              <ul className="space-y-1 text-[11.5px] leading-relaxed text-mut">
                {groups
                  .flatMap((g) => (g.latestRationales ?? []).map((r) => ({ g, r })))
                  .slice(0, 3)
                  .map(({ g, r }, index) => (
                    <li key={`${g.promptRev}-${index}`}>
                      <span className="font-mono text-[9px] uppercase text-dim">
                        {g.role} · {g.dimension}
                      </span>{" "}
                      {r}
                    </li>
                  ))}
              </ul>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}

function TemplateEditor({ id, initial }: { id: string; initial: FlowTemplate }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: catalog } = useFlowCatalog();
  const [graphParams] = useSearchParams();
  const isNew = id === "new";
  const restoredGraph = decodeGraphState(graphParams.get("g"), id, initial.revision ?? "new");
  const [draft, setDraft] = useState(initial);
  // The graph shape is ONE state object so undo/redo snapshots stages + edges
  // together — a canvas edit is one gesture, not two independent mutations.
  const [shape, setShape] = useState<{ stages: string[][]; edges: RouteEdge[] }>(() => {
    const happy = restoredGraph?.nodes.filter((n) => n.lane === "happy");
    return {
      stages: happy?.length
        ? [...new Set(happy.map((n) => n.stage))]
            .sort((a, b) => a - b)
            .map((stage) =>
              happy
                .filter((n) => n.stage === stage)
                .sort((a, b) => a.order - b.order)
                // `member ?? role` like compileTemplate: restoring by role alone
                // dropped the `#N` slot, so a phone refresh silently collapsed a
                // compared pair back into one node.
                .map((n) => n.member ?? n.role),
            )
        : stagesOf(initial),
      edges: restoredGraph?.routes ?? initial.edges ?? [],
    };
  });
  const { stages, edges } = shape;
  const [history, setHistory] = useState<{ past: (typeof shape)[]; future: (typeof shape)[] }>({
    past: [],
    future: [],
  });
  const apply = (next: typeof shape) => {
    setHistory((h) => ({ past: [...h.past.slice(-99), shape], future: [] }));
    setShape(next);
  };
  const setStages = (next: string[][]) => apply({ stages: next, edges });
  const setEdges = (next: RouteEdge[]) => apply({ stages, edges: next });
  const undo = () => {
    const prev = history.past[history.past.length - 1];
    if (!prev) return;
    setHistory((h) => ({ past: h.past.slice(0, -1), future: [...h.future, shape] }));
    setShape(prev);
  };
  const redo = () => {
    const next = history.future[history.future.length - 1];
    if (!next) return;
    setHistory((h) => ({ past: [...h.past, shape], future: h.future.slice(0, -1) }));
    setShape(next);
  };
  // The server's last rejection is stashed with a signature of the graph it
  // rejected. Any edit changes the signature, so stale red highlights clear on
  // ANY change (tap controls, edge edits, field edits) — no setState-in-effect.
  const [serverReject, setServerReject] = useState<{ sig: string; issues: GraphIssue[] } | null>(
    null,
  );
  const [selectedNodeId, setSelectedNodeId] = useState<string>();

  useEffect(() => {
    if (!catalog) return;
    const current = templateToDraft({ ...draft, nodes: stages.flat(), stages, edges });
    current.template = { id, revision: initial.revision };
    const next = new URLSearchParams(window.location.search);
    next.set("g", encodeGraphState(current));
    const timer = window.setTimeout(
      () =>
        window.history.replaceState(
          window.history.state,
          "",
          `${window.location.pathname}?${next}`,
        ),
      150,
    );
    return () => window.clearTimeout(timer);
  }, [catalog, draft, stages, edges, id, initial.revision]);

  const dirty =
    JSON.stringify({ ...draft, stages, edges }) !==
    JSON.stringify({ ...initial, stages: stagesOf(initial), edges: initial.edges ?? [] });
  useEffect(() => {
    setNavGuard(dirty ? "Unsaved automation template changes — discard them?" : null);
    return () => setNavGuard(null);
  }, [dirty]);

  const save = useMutation({
    mutationFn: () =>
      api.saveFlowTemplate(draft.id, { ...draft, nodes: stages.flat(), stages, edges }),
    onMutate: () => setServerReject(null),
    onError: (error) => {
      if (
        error instanceof ApiError &&
        error.body &&
        typeof error.body === "object" &&
        "issues" in error.body
      ) {
        const issues = (error.body as { issues?: unknown }).issues;
        if (Array.isArray(issues))
          setServerReject({
            sig: JSON.stringify({ stages, edges }),
            issues: issues as GraphIssue[],
          });
      }
    },
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: qk.flowCatalog }).then(() => navigate("/automations")),
  });
  const reset = useMutation({
    mutationFn: () => api.resetFlowTemplate(draft.id),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: qk.flowCatalog }).then(() => navigate("/automations")),
  });
  const graphDraft = templateToDraft({ ...draft, nodes: stages.flat(), stages, edges });
  const move = (index: number, delta: number) => {
    const next = [...stages];
    const target = index + delta;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    setStages(next);
  };
  const moveGraphNode = (nodeId: string, targetStage: number, targetOrder: number) => {
    const occurrence = graphDraft.nodes.find((node) => node.id === nodeId && node.lane === "happy");
    if (!occurrence) return;
    const next = stages.map((stage) => [...stage]);
    const sourceStage = next[occurrence.stage];
    if (!sourceStage) return;
    const [role] = sourceStage.splice(occurrence.order, 1);
    if (!role) return;
    const removedStage = sourceStage.length === 0;
    if (removedStage) next.splice(occurrence.stage, 1);
    const adjustedStage = Math.min(
      removedStage && targetStage > occurrence.stage ? targetStage - 1 : targetStage,
      next.length,
    );
    let landedOrder = 0;
    if (adjustedStage === next.length) next.push([role]);
    else {
      const destination = next[adjustedStage];
      if (destination.length >= 4) return;
      landedOrder = Math.min(targetOrder, destination.length);
      destination.splice(landedOrder, 0, role);
    }
    setStages(next);
    // Node ids encode position — reselect the node at its landing spot so the
    // selection ring (and keyboard focus) follows the move.
    if (selectedNodeId === nodeId) setSelectedNodeId(`h:${adjustedStage}:${landedOrder}:${role}`);
  };
  const findHappy = (nodeId: string) =>
    graphDraft.nodes.find((node) => node.id === nodeId && node.lane === "happy");
  const removeNode = (nodeId: string) => {
    const node = graphDraft.nodes.find((n) => n.id === nodeId);
    if (!node) return;
    if (node.lane === "happy") {
      setStages(
        stages
          .map((s, si) => (si === node.stage ? s.filter((_, mi) => mi !== node.order) : s))
          .filter((s) => s.length > 0),
      );
    } else {
      // Route node ids are r:<edge>:<append>:<role>.
      const [, ei, oi] = nodeId.split(":");
      const edge = edges[Number(ei)];
      if (!edge) return;
      const nextAppend = edge.append.filter((_, ai) => ai !== Number(oi));
      setEdges(
        nextAppend.length
          ? edges.map((ed, i) => (i === Number(ei) ? { ...ed, append: nextAppend } : ed))
          : edges.filter((_, i) => i !== Number(ei)),
      );
    }
    setSelectedNodeId(undefined);
  };
  // A duplicate lands as the next member SLOT (`role#2`) rather than a second
  // plain member: the slot is the key `memberRuntime` pins, so the copy can run
  // a different engine or model on the very same prompt (ADR-0023).
  const duplicateParallel = (nodeId: string) => {
    const node = findHappy(nodeId);
    if (!node || stages[node.stage].length >= 4) return;
    const member = nextMemberSlot(stages[node.stage], node.role);
    if (!member) return;
    setStages(stages.map((s, si) => (si === node.stage ? [...s, member] : s)));
    setSelectedNodeId(`h:${node.stage}:${stages[node.stage].length}:${member}`);
  };
  const addRouteFrom = (nodeId: string) => {
    const node = findHappy(nodeId);
    if (!node) return;
    setEdges([...edges, { node: node.role, verdict: "needs-work", append: ["amend", "validate"] }]);
  };
  const insertRole = (nodeId: string, role: string, side: "after" | "before") => {
    const node = findHappy(nodeId);
    const at = node ? node.stage + (side === "after" ? 1 : 0) : stages.length;
    const next = stages.map((s) => [...s]);
    next.splice(at, 0, [role]);
    setStages(next);
    setSelectedNodeId(`h:${at}:0:${role}`);
  };
  const moveVertical = (nodeId: string, delta: number) => {
    const node = findHappy(nodeId);
    if (!node) return;
    if (stages[node.stage].length === 1) {
      // Alone in its stage: swap the whole stage with its neighbor.
      const target = node.stage + delta;
      if (target < 0 || target >= stages.length) return;
      const next = stages.map((s) => [...s]);
      [next[node.stage], next[target]] = [next[target], next[node.stage]];
      setStages(next);
      setSelectedNodeId(`h:${target}:0:${node.role}`);
    } else {
      // Parallel member: extract it into its own new stage before/after.
      const next = stages.map((s) => [...s]);
      next[node.stage].splice(node.order, 1);
      const at = node.stage + (delta > 0 ? 1 : 0);
      next.splice(at, 0, [node.role]);
      setStages(next);
      setSelectedNodeId(`h:${at}:0:${node.role}`);
    }
  };
  const reorderNode = (nodeId: string, delta: number) => {
    const node = findHappy(nodeId);
    if (!node) return;
    const target = node.order + delta;
    if (target < 0 || target >= stages[node.stage].length) return;
    const next = stages.map((s) => [...s]);
    [next[node.stage][node.order], next[node.stage][target]] = [
      next[node.stage][target],
      next[node.stage][node.order],
    ];
    setStages(next);
    setSelectedNodeId(`h:${node.stage}:${target}:${node.role}`);
  };
  // Canvas keyboard model (rebound every render so closures stay fresh):
  // ⌘Z/⌘⇧Z/⌃Y history, ⌘D duplicate-in-parallel, Del remove, arrows move the
  // selected node between stages (↑↓) and within its stage (←→).
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.tagName === "SELECT" ||
          target.isContentEditable)
      )
        return;
      const mod = e.metaKey || e.ctrlKey;
      const key = e.key.toLowerCase();
      if (mod && key === "z") {
        e.preventDefault();
        if (e.shiftKey) redo();
        else undo();
        return;
      }
      if (mod && key === "y") {
        e.preventDefault();
        redo();
        return;
      }
      if (!selectedNodeId) return;
      if (mod && key === "d") {
        e.preventDefault();
        duplicateParallel(selectedNodeId);
        return;
      }
      if (e.key === "Backspace" || e.key === "Delete") {
        e.preventDefault();
        removeNode(selectedNodeId);
        return;
      }
      if (e.key === "Escape") {
        setSelectedNodeId(undefined);
        return;
      }
      if (e.key === "ArrowUp" || e.key === "ArrowDown") {
        e.preventDefault();
        moveVertical(selectedNodeId, e.key === "ArrowUp" ? -1 : 1);
        return;
      }
      if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
        e.preventDefault();
        reorderNode(selectedNodeId, e.key === "ArrowLeft" ? -1 : 1);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
  // Edges route on a ROLE, so member slots collapse to their base role here.
  const stageRoles = [...new Set(stages.flat().map((member) => parseMember(member).role))];
  const shaped: FlowTemplate = { ...draft, nodes: stages.flat(), stages, edges };
  const worstCase = worstCaseSessions(shaped);
  const clientIssues = [
    ...validateDraft(graphDraft, catalog?.nodes ?? []),
    ...validateEmitters(shaped, catalog?.nodes ?? []),
  ];
  const serverIssues =
    serverReject && serverReject.sig === JSON.stringify({ stages, edges })
      ? serverReject.issues
      : [];
  const graphIssues = serverIssues.length ? serverIssues : clientIssues;
  const selectedGraphNode = graphDraft.nodes.find((node) => node.id === selectedNodeId);
  const selectedRole = selectedGraphNode?.role;
  const selectedMember = selectedGraphNode?.member;
  const selectedNode = catalog?.nodes.find((node) => node.id === selectedRole);
  const emitter = (draft.emitters ?? []).find((e) => e.node === selectedRole);
  // A fresh emitter is pre-filled with the generic contestant/judge pair when the
  // catalog has them, so turning emission on lands a valid spec, not a red form.
  const defaultEmitter = (role: string): EmitterSpec => {
    const has = (id: string) => catalog?.nodes.some((n) => n.id === id);
    const other = catalog?.nodes.find((n) => n.id !== role)?.id ?? role;
    return {
      node: role,
      max: 3,
      roles: [has("attempt") ? "attempt" : other],
      fanIn: has("judge") ? "judge" : other,
    };
  };
  const setEmitter = (spec: EmitterSpec | null) => {
    const rest = (draft.emitters ?? []).filter((e) => e.node !== selectedRole);
    const next = spec ? [...rest, spec] : rest;
    setDraft({ ...draft, emitters: next.length ? next : undefined });
  };
  const memberPin = selectedMember ? draft.memberRuntime?.[selectedMember] : undefined;
  const setMemberPin = (pin: NodeRuntimeOverride) => {
    if (!selectedMember) return;
    const next = { ...(draft.memberRuntime ?? {}) };
    // An empty pin is no pin — leaving the key behind would persist an override
    // that governs nothing and reads as a deliberate choice.
    if (Object.values(pin).every((v) => v === undefined || v === "")) delete next[selectedMember];
    else next[selectedMember] = pin;
    setDraft({ ...draft, memberRuntime: Object.keys(next).length ? next : undefined });
  };
  const total = stages.flat().length;
  const maxMinutes = stages.reduce(
    (sum, stage) =>
      sum +
      Math.max(0, ...stage.map((role) => catalog?.nodes.find((n) => n.id === role)?.minutes ?? 0)),
    0,
  );
  const valid =
    /^[a-z][a-z0-9-]{0,39}$/.test(draft.id) &&
    draft.name.trim() &&
    total > 0 &&
    clientIssues.length === 0;

  return (
    <section className="mx-auto max-w-[980px]">
      <div className="mb-5 flex items-start justify-between gap-3">
        <div>
          <Kicker>// automation template</Kicker>
          <h1 className="mt-2 font-head text-[25px] font-bold">
            {isNew ? "New template" : draft.name}
          </h1>
        </div>
        <Button size="sm" onClick={() => navigate("/automations")}>
          <ArrowLeft className="size-3.5" /> Studio
        </Button>
      </div>

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1fr)_310px]">
        <div className="min-w-0 space-y-4">
          <Card className="space-y-4 p-4 sm:p-5">
            <label className="block">
              <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                Template id
              </span>
              <Input
                disabled={!isNew}
                value={draft.id}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    id: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""),
                  })
                }
                placeholder="secure-delivery"
              />
            </label>
            <label className="block">
              <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Name</span>
              <Input
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                placeholder="Secure delivery"
              />
            </label>
            <label className="block">
              <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                Description
              </span>
              <Textarea
                value={draft.description}
                onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                placeholder="When this automation should be used."
              />
            </label>
          </Card>

          <div>
            <SectionHeader
              className="mb-3"
              title="Stage sequence"
              meta={`${total} sessions · ${stages.length} stages`}
            />
            <div className="space-y-2">
              {stages.map((stage, index) => (
                <Card key={`${stage.join("+")}-${index}`} className="flex items-center gap-3 p-3">
                  <span className="grid size-8 shrink-0 place-items-center border border-line2 font-mono text-[10px] text-mut">
                    {index + 1}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      {stage.map((member, memberIndex) => {
                        const { role, slot } = parseMember(member);
                        const node = catalog?.nodes.find((n) => n.id === role);
                        const pin = draft.memberRuntime?.[member];
                        return (
                          <span
                            key={`${member}-${memberIndex}`}
                            className="flex items-center gap-1 border border-line2 bg-bg px-2 py-1"
                          >
                            <button
                              className="text-[12px] font-semibold"
                              onClick={() => navigate(`/automations/nodes/${role}`)}
                            >
                              {node?.name ?? role}
                              {slot ? (
                                <span className="ml-1 font-mono text-[9.5px] text-accent">
                                  #{slot}
                                </span>
                              ) : null}
                            </button>
                            {pin?.executor && (
                              <span
                                className="flex items-center gap-1 font-mono text-[9px] uppercase text-dim"
                                title={`Runs on ${pin.executor}${pin.model ? ` · ${pin.model}` : ""}`}
                              >
                                <ExecutorIcon executor={pin.executor} className="size-3" />
                                {pin.model || pin.executor}
                              </span>
                            )}
                            <button
                              aria-label={`Remove ${member} from stage ${index + 1}`}
                              className="text-dim hover:text-danger"
                              onClick={() => {
                                const next = stages
                                  .map((s, si) =>
                                    si === index ? s.filter((_, mi) => mi !== memberIndex) : s,
                                  )
                                  .filter((s) => s.length > 0);
                                setStages(next);
                              }}
                            >
                              <Trash2 className="size-3" />
                            </button>
                          </span>
                        );
                      })}
                      {stage.length > 1 && (
                        <Badge tone="accent">parallel ×{stage.length} → own worktrees</Badge>
                      )}
                    </div>
                    {stage.length < 4 && (
                      <select
                        aria-label={`Add parallel node to stage ${index + 1}`}
                        value=""
                        onChange={(e) => {
                          if (!e.target.value) return;
                          // A second member of a role already in this stage takes
                          // the next slot, so each gets its own runtime pin.
                          const member = nextMemberSlot(stage, e.target.value);
                          if (!member) return;
                          setStages(stages.map((s, si) => (si === index ? [...s, member] : s)));
                        }}
                        className="mt-1.5 border border-dashed border-line2 bg-bg px-2 py-1 font-mono text-[9px] uppercase text-dim"
                      >
                        <option value="">+ parallel</option>
                        {catalog?.nodes.map((node) => (
                          <option key={node.id} value={node.id}>
                            {node.name}
                          </option>
                        ))}
                      </select>
                    )}
                  </div>
                  <div className="flex gap-1">
                    <button
                      aria-label={`Move stage ${index + 1} up`}
                      className="tap-control"
                      disabled={index === 0}
                      onClick={() => move(index, -1)}
                    >
                      <ArrowUp className="size-3.5" />
                    </button>
                    <button
                      aria-label={`Move stage ${index + 1} down`}
                      className="tap-control"
                      disabled={index === stages.length - 1}
                      onClick={() => move(index, 1)}
                    >
                      <ArrowDown className="size-3.5" />
                    </button>
                  </div>
                </Card>
              ))}
            </div>
            <select
              aria-label="Add node"
              value=""
              onChange={(e) => e.target.value && setStages([...stages, [e.target.value]])}
              className="mt-2 min-h-11 w-full border border-dashed border-line2 bg-bg px-3 font-mono text-[10px] uppercase text-mut"
            >
              <option value="">+ Add stage</option>
              {catalog?.nodes.map((node) => (
                <option key={node.id} value={node.id}>
                  {node.name}
                </option>
              ))}
            </select>
          </div>

          <div>
            <SectionHeader className="mb-3" title="Exception edges" meta="on needs-work, append" />
            <div className="space-y-2">
              {edges.map((edge, index) => (
                <Card key={index} className="flex flex-wrap items-center gap-2 p-3">
                  <span className="font-mono text-[9.5px] uppercase text-dim">when</span>
                  <select
                    aria-label={`Edge ${index + 1} source node`}
                    value={edge.node}
                    onChange={(e) =>
                      setEdges(
                        edges.map((ed, ei) =>
                          ei === index ? { ...ed, node: e.target.value } : ed,
                        ),
                      )
                    }
                    className="min-h-9 border border-line2 bg-bg px-2 font-mono text-[10px] uppercase"
                  >
                    {stageRoles.map((role) => (
                      <option key={role} value={role}>
                        {role}
                      </option>
                    ))}
                  </select>
                  <span className="font-mono text-[9.5px] uppercase text-dim">
                    reports needs-work → append
                  </span>
                  {edge.append.map((role, appendIndex) => (
                    <span
                      key={`${role}-${appendIndex}`}
                      className="flex items-center gap-1 border border-line2 bg-bg px-2 py-1 font-mono text-[10px] uppercase"
                    >
                      {role}
                      <button
                        aria-label={`Remove ${role} from edge ${index + 1}`}
                        className="text-dim hover:text-danger"
                        onClick={() => {
                          const nextAppend = edge.append.filter((_, ai) => ai !== appendIndex);
                          setEdges(
                            nextAppend.length
                              ? edges.map((ed, ei) =>
                                  ei === index ? { ...ed, append: nextAppend } : ed,
                                )
                              : edges.filter((_, ei) => ei !== index),
                          );
                        }}
                      >
                        <Trash2 className="size-3" />
                      </button>
                    </span>
                  ))}
                  <select
                    aria-label={`Add node to edge ${index + 1}`}
                    value=""
                    onChange={(e) =>
                      e.target.value &&
                      setEdges(
                        edges.map((ed, ei) =>
                          ei === index ? { ...ed, append: [...ed.append, e.target.value] } : ed,
                        ),
                      )
                    }
                    className="min-h-9 border border-dashed border-line2 bg-bg px-2 font-mono text-[9px] uppercase text-dim"
                  >
                    <option value="">+</option>
                    {catalog?.nodes.map((node) => (
                      <option key={node.id} value={node.id}>
                        {node.name}
                      </option>
                    ))}
                  </select>
                </Card>
              ))}
            </div>
            <Button
              size="sm"
              className="mt-2"
              disabled={!stageRoles.length}
              onClick={() =>
                setEdges([
                  ...edges,
                  { node: stageRoles[0], verdict: "needs-work", append: ["amend", "validate"] },
                ])
              }
            >
              <Plus className="size-3" /> Edge
            </Button>
            <p className="mt-2 text-[11px] leading-relaxed text-dim">
              Routing is declared here as data — never in a node's prompt. Without an edge, a
              mid-chain needs-work proceeds down the happy path; the final node's needs-work runs
              the default amend → validate round. A run executes at most {RUN_SESSION_CAP} node
              sessions total, emitted nodes included.
            </p>
          </div>

          {!isNew && <ScoresSection automation={draft.id} />}
        </div>

        <div className="min-w-0 space-y-4">
          <Card className="p-3">
            <div className="mb-2 flex items-center justify-between">
              <span className="font-mono text-[10px] font-bold uppercase text-mut">
                Node library
              </span>
              <span className="text-[10px] text-dim">drag or tap to add</span>
            </div>
            <div
              className="flex gap-2 overflow-x-auto pb-1"
              role="list"
              aria-label="Node library palette"
            >
              {(catalog?.nodes ?? []).map((node) => (
                <button
                  key={node.id}
                  draggable
                  data-testid={`palette-node-${node.id}`}
                  onDragStart={(e) => {
                    e.dataTransfer.setData("application/nightshift-node", node.id);
                    e.dataTransfer.effectAllowed = "copy";
                  }}
                  onClick={() => setStages([...stages, [node.id]])}
                  className="flex min-h-11 shrink-0 items-center gap-2 border border-line2 bg-bg px-3 text-[11px] hover:border-accent"
                >
                  <NodeIcon name={node.icon} className="size-5 text-accent" />
                  {node.name}
                </button>
              ))}
            </div>
          </Card>
          <Card className="p-4">
            <label className="flex min-h-11 items-center justify-between gap-3">
              <span>
                <b className="block text-[12px]">Recurring night run</b>
                <small className="text-dim">
                  A schedule makes this automation a recurring night run.
                </small>
              </span>
              <input
                type="checkbox"
                aria-label="Recurring night run"
                checked={Boolean(draft.schedule)}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    schedule: e.target.checked
                      ? { agent: "playground", repo: "", goal: draft.name, time: "22:00" }
                      : undefined,
                  })
                }
              />
            </label>
            {draft.schedule && (
              <div className="mt-3 grid gap-2">
                <div className="grid grid-cols-2 gap-2">
                  <Input
                    aria-label="Schedule agent"
                    value={draft.schedule.agent}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        schedule: { ...draft.schedule!, agent: e.target.value },
                      })
                    }
                  />
                  <Input
                    aria-label="Schedule repository"
                    placeholder="repository"
                    value={draft.schedule.repo}
                    onChange={(e) =>
                      setDraft({ ...draft, schedule: { ...draft.schedule!, repo: e.target.value } })
                    }
                  />
                </div>
                <Input
                  aria-label="Schedule goal"
                  placeholder="goal"
                  value={draft.schedule.goal}
                  onChange={(e) =>
                    setDraft({ ...draft, schedule: { ...draft.schedule!, goal: e.target.value } })
                  }
                />
                <Input
                  aria-label="Daily time"
                  type="time"
                  value={draft.schedule.time}
                  onChange={(e) =>
                    setDraft({ ...draft, schedule: { ...draft.schedule!, time: e.target.value } })
                  }
                />
                <p className="text-[10px] leading-relaxed text-dim">
                  It starts daily in Asia/Bishkek. If its previous run is still queued or running,
                  this firing is skipped and raised as an alert; runs never overlap.
                </p>
              </div>
            )}
          </Card>
          <Card className="p-4">
            <div className="mb-3 flex items-center justify-between">
              <span className="font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                Canvas
              </span>
              <div className="flex gap-1">
                <button
                  className="tap-control"
                  aria-label="Undo"
                  title="Undo (⌘Z)"
                  disabled={history.past.length === 0}
                  onClick={undo}
                >
                  <Undo2 className="size-3.5" />
                </button>
                <button
                  className="tap-control"
                  aria-label="Redo"
                  title="Redo (⌘⇧Z)"
                  disabled={history.future.length === 0}
                  onClick={redo}
                >
                  <Redo2 className="size-3.5" />
                </button>
              </div>
            </div>
            <FlowGraph
              draft={graphDraft}
              catalog={catalog?.nodes ?? []}
              mode="edit"
              issues={graphIssues}
              selected={selectedNodeId ?? null}
              onSelect={setSelectedNodeId}
              onMoveNode={moveGraphNode}
              onDropRole={(role) => setStages([...stages, [role]])}
              onRemoveNode={removeNode}
              onDuplicateNode={duplicateParallel}
              onAddRouteFrom={addRouteFrom}
              onInsertRole={insertRole}
              detail={
                selectedNode ? (
                  <div>
                    <b className="font-head text-[14px]">{selectedNode.name}</b>
                    <p className="mt-1 text-[11px] leading-relaxed text-mut">
                      {selectedNode.description}
                    </p>
                    <p className="mt-2 font-mono text-[9.5px] uppercase text-dim">
                      {selectedNode.effort} · {selectedNode.minutes}m ·{" "}
                      {selectedNode.executor || "claude"}
                    </p>
                  </div>
                ) : undefined
              }
            />
            <p className="mt-2 hidden font-mono text-[9px] uppercase tracking-wide text-dim sm:block">
              click select · drag move · drag a handle to insert · del · ⌘z · ⌘d · f fullscreen
            </p>
          </Card>
          {selectedNode && (
            <Card className="p-4" data-testid="graph-node-inspector">
              <div className="flex items-start gap-3">
                <NodeIcon name={selectedNode.icon} className="size-7 shrink-0 text-accent" />
                <div className="min-w-0 flex-1">
                  <div className="font-head text-[15px] font-bold">{selectedNode.name}</div>
                  <p className="mt-1 text-[11px] leading-relaxed text-mut">
                    {selectedNode.description}
                  </p>
                </div>
              </div>
              <dl className="mt-3 grid grid-cols-2 gap-2 border-t border-line pt-3 font-mono text-[9.5px] uppercase text-dim">
                <div>
                  <dt>Runtime</dt>
                  <dd className="mt-1 text-ink">
                    {selectedNode.effort} · {selectedNode.minutes}m
                  </dd>
                </div>
                <div>
                  <dt>Executor</dt>
                  <dd className="mt-1 flex items-center gap-1.5 text-ink">
                    <ExecutorIcon executor={selectedNode.executor} className="size-3.5" />
                    {selectedNode.executor || "claude"}
                  </dd>
                </div>
                <div>
                  <dt>Prompt</dt>
                  <dd className="mt-1 truncate text-ink">{selectedNode.promptRevision}</dd>
                </div>
                <div>
                  <dt>Source</dt>
                  <dd className="mt-1 text-ink">{selectedNode.promptSource}</dd>
                </div>
              </dl>
              <Button
                size="block"
                className="mt-3"
                onClick={() => navigate(`/automations/nodes/${selectedNode.id}`)}
              >
                <FileCode2 className="size-4" /> Edit prompt, runtime & definition
              </Button>
            </Card>
          )}
          {selectedNode && selectedRole && (
            <Card className="p-4" data-testid="node-emits-panel">
              <div className="mb-2 font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                Emits · {selectedNode.name}
              </div>
              <label className="flex min-h-11 items-center justify-between gap-3">
                <span>
                  <b className="block text-[12px]">Produces nodes at runtime</b>
                  <small className="text-dim">
                    This node may decide how many successors the run needs — up to the cap below.
                  </small>
                </span>
                <input
                  type="checkbox"
                  aria-label={`${selectedNode.name} produces nodes at runtime`}
                  checked={Boolean(emitter)}
                  onChange={(e) =>
                    setEmitter(e.target.checked ? defaultEmitter(selectedRole) : null)
                  }
                />
              </label>
              {emitter && (
                <div className="mt-3 space-y-3">
                  <label className="block">
                    <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                      Most nodes it may produce
                    </span>
                    <Input
                      type="number"
                      min={1}
                      max={EMITTER_MAX_WIDTH}
                      aria-label="Emission cap"
                      value={emitter.max}
                      onChange={(e) =>
                        setEmitter({ ...emitter, max: Math.trunc(Number(e.target.value)) })
                      }
                    />
                  </label>
                  <div>
                    <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                      Nodes it may produce
                    </span>
                    <div className="flex flex-wrap gap-1.5">
                      {(catalog?.nodes ?? []).map((node) => {
                        const on = emitter.roles.includes(node.id);
                        return (
                          <button
                            key={node.id}
                            aria-pressed={on}
                            onClick={() =>
                              setEmitter({
                                ...emitter,
                                roles: on
                                  ? emitter.roles.filter((r) => r !== node.id)
                                  : [...emitter.roles, node.id],
                              })
                            }
                            className={cn(
                              "min-h-11 border px-2 text-[11px]",
                              on
                                ? "border-accent bg-accent/10 text-ink"
                                : "border-line2 bg-bg text-mut",
                            )}
                          >
                            {node.name}
                          </button>
                        );
                      })}
                    </div>
                  </div>
                  <label className="block">
                    <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                      Node that consumes the group
                    </span>
                    <select
                      aria-label="Fan-in node"
                      value={emitter.fanIn}
                      onChange={(e) => setEmitter({ ...emitter, fanIn: e.target.value })}
                      className="min-h-11 w-full border border-line2 bg-bg px-3 font-mono text-[11px] uppercase text-ink focus:border-accent"
                    >
                      {(catalog?.nodes ?? []).map((node) => (
                        <option key={node.id} value={node.id}>
                          {node.name}
                        </option>
                      ))}
                    </select>
                  </label>
                  <p className="text-[11px] leading-relaxed text-dim">
                    The produced nodes run in parallel, each in its own worktree, and the fan-in
                    node starts as soon as at least one of them reports. Anything this node asks for
                    beyond the cap — or a node it was never allowed to produce — is refused and
                    raised as an alert, never silently applied.
                  </p>
                </div>
              )}
            </Card>
          )}
          {selectedNode && selectedMember && (
            <Card className="p-4" data-testid="member-runtime-panel">
              <div className="mb-1 font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                This member · <span className="text-accent">{selectedMember}</span>
              </div>
              <p className="mb-3 text-[11px] leading-relaxed text-dim">
                Pins for this member only. Members of one role share the prompt revision, so two of
                them on different engines is a controlled comparison.
              </p>
              <div className="grid grid-cols-2 gap-2">
                <label className="block">
                  <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                    Executor
                  </span>
                  <select
                    aria-label="Member executor"
                    value={memberPin?.executor ?? ""}
                    onChange={(e) =>
                      setMemberPin({ ...memberPin, executor: e.target.value || undefined })
                    }
                    className="min-h-11 w-full border border-line2 bg-bg px-2 font-mono text-[11px] uppercase text-ink focus:border-accent"
                  >
                    <option value="">inherit</option>
                    <option value="claude">claude</option>
                    <option value="codex">codex</option>
                  </select>
                </label>
                <label className="block">
                  <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                    Effort
                  </span>
                  <select
                    aria-label="Member effort"
                    value={memberPin?.effort ?? ""}
                    onChange={(e) =>
                      setMemberPin({ ...memberPin, effort: e.target.value || undefined })
                    }
                    className="min-h-11 w-full border border-line2 bg-bg px-2 font-mono text-[11px] uppercase text-ink focus:border-accent"
                  >
                    <option value="">inherit</option>
                    {["low", "medium", "high", "xhigh", "max"].map((value) => (
                      <option key={value} value={value}>
                        {value}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              <label className="mt-2 block">
                <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                  Model · optional
                </span>
                <Input
                  aria-label="Member model"
                  value={memberPin?.model ?? ""}
                  placeholder="executor default"
                  onChange={(e) =>
                    setMemberPin({ ...memberPin, model: e.target.value.trim() || undefined })
                  }
                />
              </label>
              <label className="mt-2 block">
                <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
                  Window · minutes
                </span>
                <Input
                  type="number"
                  min={10}
                  max={480}
                  aria-label="Member window minutes"
                  value={memberPin?.minutes ?? ""}
                  placeholder={`${selectedNode.minutes}`}
                  onChange={(e) =>
                    setMemberPin({
                      ...memberPin,
                      minutes: e.target.value ? Number(e.target.value) : undefined,
                    })
                  }
                />
              </label>
              <Button
                size="block"
                className="mt-3"
                disabled={!memberPin}
                onClick={() => setMemberPin({})}
              >
                <RotateCcw className="size-4" /> Clear this member's pins
              </Button>
            </Card>
          )}
          <Card className="p-4">
            <div className="mb-3 font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
              Validation
            </div>
            <div className="space-y-2 text-[12px]">
              {graphIssues.length > 0 && (
                <div
                  className="border border-danger/60 bg-danger/10 p-3 text-danger"
                  role="alert"
                  aria-live="assertive"
                >
                  <b className="block">
                    {serverIssues.length
                      ? "The server rejected this graph"
                      : "This graph cannot run yet"}
                  </b>
                  <ul className="mt-1 list-disc space-y-1 pl-4">
                    {graphIssues.map((issue, index) => (
                      <li key={`${issue.path}-${index}`}>{issue.message}</li>
                    ))}
                  </ul>
                </div>
              )}
              <div className="flex justify-between">
                <span className="text-mut">Worst-case runtime</span>
                <b>
                  {Math.floor(maxMinutes / 60)}h {maxMinutes % 60}m
                </b>
              </div>
              <div className="flex justify-between">
                <span className="text-mut">Fresh sessions</span>
                <b>{total}</b>
              </div>
              {(draft.emitters ?? []).length > 0 && (
                <div className="flex justify-between">
                  <span className="text-mut">Worst case with emissions</span>
                  <b className={worstCase > RUN_SESSION_CAP ? "text-danger" : undefined}>
                    {worstCase} / {RUN_SESSION_CAP}
                  </b>
                </div>
              )}
              <div className="flex justify-between">
                <span className="text-mut">Runtime emitters</span>
                <b>{(draft.emitters ?? []).length}</b>
              </div>
              <div className="flex justify-between">
                <span className="text-mut">Member pins</span>
                <b>{Object.keys(draft.memberRuntime ?? {}).length}</b>
              </div>
              <div className="flex justify-between">
                <span className="text-mut">Parallel stages</span>
                <b>{stages.filter((stage) => stage.length > 1).length}</b>
              </div>
              <div className="flex justify-between">
                <span className="text-mut">Exception edges</span>
                <b>{edges.length}</b>
              </div>
              <div className="flex justify-between">
                <span className="text-mut">Acceptance loop</span>
                <b>{stages.flat().includes("validate") ? "enabled" : "none"}</b>
              </div>
            </div>
          </Card>
          <Card className="p-4 text-[11.5px] leading-relaxed text-mut">
            Each run pins this template's node order and each node's prompt revision. Editing the
            template never changes work already in flight.
          </Card>
          <Button
            variant="accent"
            size="block"
            disabled={!valid || save.isPending}
            onClick={() => save.mutate()}
          >
            <Save className="size-4" /> {save.isPending ? "Saving…" : "Save revision"}
          </Button>
          {!isNew && draft.customized && (
            <Button size="block" disabled={reset.isPending} onClick={() => reset.mutate()}>
              <RotateCcw className="size-4" />{" "}
              {draft.builtin ? "Reset to built-in" : "Delete template"}
            </Button>
          )}
          {!isNew && (
            <Button
              size="block"
              onClick={() =>
                navigate(`/automations/templates/new?from=${encodeURIComponent(draft.id)}`)
              }
            >
              <GitCompare className="size-4" /> Duplicate as new
            </Button>
          )}
          <ErrorText>
            {save.error instanceof Error
              ? save.error.message
              : reset.error instanceof Error
                ? reset.error.message
                : null}
          </ErrorText>
        </div>
      </div>
    </section>
  );
}

export function TemplateLibrary() {
  const { id } = useParams();
  const [params] = useSearchParams();
  const { data: catalog } = useFlowCatalog();
  if (id) {
    if (!catalog) return <EmptyText>Loading template…</EmptyText>;
    const from = params.get("from");
    const source = from ? catalog.templates.find((template) => template.id === from) : undefined;
    // A copy keeps the whole shape — stages, routes, emitters, member pins —
    // because a stage-only automation has no `nodes` and copying that field
    // alone would silently produce an empty template. The schedule is dropped
    // on purpose: two automations on one time would fire the same work twice.
    const initial: FlowTemplate | undefined =
      id === "new"
        ? source
          ? {
              ...source,
              id: "",
              name: `${source.name} copy`,
              builtin: false,
              customized: false,
              revision: undefined,
              schedule: undefined,
            }
          : {
              id: "",
              name: "",
              description: "",
              nodes: ["plan", "implement", "review", "validate"],
            }
        : catalog.templates.find((template) => template.id === id);
    if (!initial) return <EmptyText>Template not found.</EmptyText>;
    return <TemplateEditor key={`${id}-${initial.revision ?? "new"}`} id={id} initial={initial} />;
  }
  return <Automations />;
}

function PromptEditor({ role }: { role: string }) {
  const { data: catalog } = useFlowCatalog();
  const node = catalog?.nodes.find((candidate) => candidate.id === role);
  const prompt = usePromptDocument(`node-${role}`);
  if (prompt.isLoading) return <EmptyText>Loading node prompt…</EmptyText>;
  if (!node || !prompt.data)
    return (
      <ErrorText>
        {prompt.error instanceof Error ? prompt.error.message : "Node not found"}
      </ErrorText>
    );
  return (
    <PromptEditorForm
      key={`${role}-${prompt.data.revision}`}
      role={role}
      node={node}
      doc={prompt.data}
    />
  );
}

function PromptEditorForm({
  role,
  node,
  doc,
}: {
  role: string;
  node: FlowNodeDefinition;
  doc: PromptDocument;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [body, setBody] = useState(doc.body);
  const [tab, setTab] = useState<"prompt" | "runtime" | "history">("prompt");
  const dirty = body !== doc.body;
  useEffect(() => {
    setNavGuard(dirty ? "Unsaved node prompt changes — discard them?" : null);
    return () => setNavGuard(null);
  }, [dirty]);

  const save = useMutation({
    mutationFn: (next: string) => api.savePrompt(`node-${role}`, next),
    onSuccess: (doc) => {
      setBody(doc.body);
      qc.setQueryData(qk.prompt(`node-${role}`), doc);
      qc.invalidateQueries({ queryKey: qk.flowCatalog });
      qc.invalidateQueries({ queryKey: qk.prompts });
    },
  });
  const restore = useMutation({
    mutationFn: (version: string) => api.restorePrompt(`node-${role}`, version),
    onSuccess: (doc) => {
      setBody(doc.body);
      qc.setQueryData(qk.prompt(`node-${role}`), doc);
      qc.invalidateQueries({ queryKey: qk.flowCatalog });
    },
  });
  const remove = useMutation({
    mutationFn: () => api.deleteNode(role),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: qk.flowCatalog }).then(() => navigate("/automations/nodes")),
  });

  return (
    <section>
      <div className="mb-5 flex items-start justify-between gap-3">
        <div>
          <div className="mb-2 flex flex-wrap items-center gap-2">
            {node.custom ? (
              <Badge tone="review">operator-defined</Badge>
            ) : (
              <Badge tone={doc.source === "custom" ? "accent" : "muted"}>{doc.source}</Badge>
            )}
            <span className="font-mono text-[9.5px] uppercase text-dim">
              revision {doc.revision}
            </span>
            <span className="inline-flex items-center gap-1.5 font-mono text-[9.5px] uppercase text-dim">
              <ExecutorIcon executor={node.executor} className="size-3.5" />
              executor {node.executor || "claude"}
            </span>
          </div>
          <h1 className="font-head text-[26px] font-bold tracking-tight">{node.name}</h1>
          <p className="mt-1 text-[12.5px] text-mut">{node.description}</p>
        </div>
        <div className="flex gap-2">
          {node.custom && (
            <Button
              size="sm"
              className="hover:border-danger hover:text-danger"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              <Trash2 className="size-3.5" /> Delete
            </Button>
          )}
          <Button size="sm" onClick={() => navigate("/automations")}>
            <ArrowLeft className="size-3.5" /> Studio
          </Button>
        </div>
      </div>
      <ErrorText>{remove.error instanceof Error ? remove.error.message : null}</ErrorText>

      <div className="mb-4 flex border-b border-line" role="tablist">
        {(["prompt", "runtime", "history"] as const).map((name) => (
          <button
            key={name}
            role="tab"
            aria-selected={tab === name}
            onClick={() => setTab(name)}
            className={cn(
              "min-h-11 border-b-2 px-4 font-mono text-[10px] font-bold uppercase tracking-wider",
              tab === name ? "border-accent text-ink" : "border-transparent text-mut",
            )}
          >
            {name}
          </button>
        ))}
      </div>

      {tab === "prompt" && (
        <div className="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,1fr)_360px]">
          <div>
            <div className="mb-2 flex items-center justify-between">
              <span className="font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                Instruction body
              </span>
              <span className="font-mono text-[9.5px] text-dim">
                {body.length.toLocaleString()} chars
              </span>
            </div>
            <Textarea
              aria-label="Node prompt body"
              className="min-h-[430px] resize-y font-mono text-[12px] leading-relaxed"
              value={body}
              onChange={(e) => setBody(e.target.value)}
            />
            <div className="mt-3 flex flex-wrap gap-2">
              <Button
                variant="accent"
                size="md"
                disabled={!dirty || !body.trim() || save.isPending}
                onClick={() => save.mutate(body)}
              >
                <Save className="size-4" /> Save prompt revision
              </Button>
              {doc.source === "custom" && !!doc.defaultBody && (
                <Button
                  size="md"
                  disabled={save.isPending}
                  onClick={() => save.mutate(doc.defaultBody ?? "")}
                >
                  <RotateCcw className="size-4" /> Revert to built-in
                </Button>
              )}
            </div>
            <ErrorText>{save.error instanceof Error ? save.error.message : null}</ErrorText>
          </div>
          <div className="space-y-4">
            <Card className="p-4">
              <div className="mb-3 flex items-center gap-2 font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                <Blocks className="size-3.5" /> Effective prompt stack
              </div>
              {[
                "Global contract",
                "Repository CLAUDE.md",
                `${node.name} prompt · ${doc.revision}`,
                "Run goal + acceptance",
                "Upstream report paths",
              ].map((layer, i) => (
                <div
                  key={layer}
                  className="flex gap-2 border-t border-line/60 py-2.5 first:border-0"
                >
                  <span className="font-mono text-[9px] text-dim">
                    {String(i + 1).padStart(2, "0")}
                  </span>
                  <span className="text-[11.5px]">{layer}</span>
                </div>
              ))}
            </Card>
            {!!doc.defaultBody && (
              <details className="border border-line bg-surface">
                <summary className="cursor-pointer px-4 py-3 font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                  <GitCompare className="mr-2 inline size-3.5" /> Compare built-in
                </summary>
                <pre className="max-h-[300px] overflow-auto whitespace-pre-wrap border-t border-line bg-bg p-3 font-mono text-[10.5px] leading-relaxed text-mut">
                  {doc.defaultBody}
                </pre>
              </details>
            )}
            <div className="break-all font-mono text-[9px] leading-relaxed text-dim">
              {doc.path}
            </div>
          </div>
        </div>
      )}

      {tab === "runtime" && <RuntimePanel key={node.runtimeRevision} role={role} node={node} />}

      {tab === "history" && (
        <Card className="px-4 py-1">
          {doc.versions.length ? (
            doc.versions.map((version) => (
              <div
                key={version.id}
                className="flex min-h-14 items-center gap-3 border-b border-line/60 py-3 last:border-0"
              >
                <History className="size-4 shrink-0 text-mut" />
                <div className="min-w-0 flex-1">
                  <div className="font-mono text-[10.5px] text-ink">
                    revision {version.revision}
                  </div>
                  <div className="mt-0.5 font-mono text-[9px] uppercase text-dim">
                    {fmtAt(version.at * 1000)} · {version.size.toLocaleString()} bytes
                  </div>
                </div>
                <Button
                  size="sm"
                  disabled={restore.isPending}
                  onClick={() => restore.mutate(version.id)}
                >
                  Restore
                </Button>
              </div>
            ))
          ) : (
            <EmptyText>
              No earlier revisions yet. The current body is the first saved revision.
            </EmptyText>
          )}
          <ErrorText>{restore.error instanceof Error ? restore.error.message : null}</ErrorText>
        </Card>
      )}
    </section>
  );
}

function RuntimePanel({ role, node }: { role: string; node: FlowNodeDefinition }) {
  const qc = useQueryClient();
  const [effort, setEffort] = useState(node.effort);
  const [minutes, setMinutes] = useState(node.minutes);
  const [executor, setExecutor] = useState(node.executor || "claude");
  const [model, setModel] = useState(node.model || "");
  const save = useMutation({
    mutationFn: () => api.saveNodeRuntime(role, effort, minutes, executor, model.trim()),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.flowCatalog }),
  });
  const reset = useMutation({
    mutationFn: () => api.resetNodeRuntime(role),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.flowCatalog }),
  });
  const dirty =
    effort !== node.effort ||
    minutes !== node.minutes ||
    executor !== (node.executor || "claude") ||
    model.trim() !== (node.model || "");
  return (
    <div className="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
      <Card className="space-y-4 p-4 sm:p-5">
        <div className="flex items-center justify-between gap-3">
          <div className="font-head text-[16px] font-bold">Session runtime defaults</div>
          <Badge tone={node.runtimeSource === "custom" ? "accent" : "muted"}>
            {node.runtimeSource}
          </Badge>
        </div>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase tracking-wider text-mut">
            Reasoning effort
          </span>
          <select
            value={effort}
            onChange={(e) => setEffort(e.target.value)}
            className="min-h-11 w-full border border-line2 bg-bg px-3 font-mono text-[11px] uppercase text-ink focus:border-accent"
          >
            {["low", "medium", "high", "xhigh", "max"].map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </select>
        </label>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase tracking-wider text-mut">
            Auto-stop ceiling · minutes
          </span>
          <Input
            type="number"
            min={10}
            max={480}
            value={minutes}
            onChange={(e) => setMinutes(Number(e.target.value))}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase tracking-wider text-mut">
            Executor
          </span>
          <select
            value={executor}
            onChange={(e) => setExecutor(e.target.value)}
            className="min-h-11 w-full border border-line2 bg-bg px-3 font-mono text-[11px] uppercase text-ink focus:border-accent"
            title="claude = Claude Code; codex = OpenAI Codex CLI on the operator's ChatGPT plan (ADR-0018)"
          >
            {["claude", "codex"].map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </select>
        </label>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase tracking-wider text-mut">
            Model · optional
          </span>
          <Input
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="executor default"
            title="Exact model id (e.g. claude-fable-5, gpt-5.6-terra). Must belong to the executor's family; empty keeps the scheduler default."
          />
        </label>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="accent"
            size="md"
            disabled={!dirty || minutes < 10 || minutes > 480 || save.isPending}
            onClick={() => save.mutate()}
          >
            <Save className="size-4" /> Save runtime
          </Button>
          {node.runtimeSource === "custom" && (
            <Button size="md" disabled={reset.isPending} onClick={() => reset.mutate()}>
              <RotateCcw className="size-4" /> Reset defaults
            </Button>
          )}
        </div>
        <ErrorText>
          {save.error instanceof Error
            ? save.error.message
            : reset.error instanceof Error
              ? reset.error.message
              : null}
        </ErrorText>
      </Card>
      <div className="space-y-3">
        <Card className="p-4">
          <Timer className="mb-3 size-4 text-accent" />
          <div className="font-head text-[22px] font-bold">{minutes}m</div>
          <div className="font-mono text-[9.5px] uppercase text-dim">maximum session window</div>
        </Card>
        <Card className="p-4 text-[11.5px] leading-relaxed text-mut">
          Built-in: {node.defaultEffort} · {node.defaultMinutes}m. A run may shorten this window to
          meet its absolute deadline; it never silently extends it.
        </Card>
        <Card className="p-4">
          <div className="mb-1 font-mono text-[9.5px] uppercase text-dim">Output contract</div>
          <div className="text-[12px] font-semibold leading-relaxed">{node.output}</div>
        </Card>
      </div>
    </div>
  );
}

function NodeCreator() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [def, setDef] = useState({
    id: "",
    name: "",
    description: "",
    effort: "high",
    minutes: 90,
    output: "",
    prompt: "",
    executor: "claude",
    model: "",
    icon: "architect",
  });
  const create = useMutation({
    mutationFn: () => api.createNode({ ...def, model: def.model.trim() || undefined }),
    onSuccess: () =>
      qc
        .invalidateQueries({ queryKey: qk.flowCatalog })
        .then(() => navigate(`/automations/nodes/${def.id}`)),
  });
  const valid =
    /^[a-z][a-z0-9-]{1,31}$/.test(def.id) &&
    def.name.trim() &&
    def.prompt.trim() &&
    def.minutes >= 10 &&
    def.minutes <= 480;
  return (
    <section className="mx-auto max-w-[760px]">
      <div className="mb-5 flex items-start justify-between gap-3">
        <div>
          <Kicker>// new node</Kicker>
          <h1 className="mt-2 font-head text-[25px] font-bold">Define a session role</h1>
          <p className="mt-1 max-w-[60ch] text-[12.5px] text-mut">
            A node is a reusable, task-agnostic role. Its prompt defines only the role — the run
            supplies the goal, acceptance criteria, and upstream reports at mint time.
          </p>
        </div>
        <Button size="sm" onClick={() => navigate("/automations/nodes")}>
          <ArrowLeft className="size-3.5" /> Library
        </Button>
      </div>
      <Card className="space-y-4 p-4 sm:p-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Node id</span>
            <Input
              value={def.id}
              onChange={(e) =>
                setDef({ ...def, id: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "") })
              }
              placeholder="security-audit"
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Name</span>
            <Input
              value={def.name}
              onChange={(e) => setDef({ ...def, name: e.target.value })}
              placeholder="Security audit"
            />
          </label>
        </div>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Description</span>
          <Input
            value={def.description}
            onChange={(e) => setDef({ ...def, description: e.target.value })}
            placeholder="Adversarial security pass over the run branch."
          />
        </label>
        <fieldset>
          <legend className="mb-2 font-mono text-[10px] uppercase text-mut">Icon preset</legend>
          <div className="grid grid-cols-6 gap-2">
            {CUSTOM_ICON_CHOICES.map((icon) => (
              <button
                type="button"
                key={icon}
                aria-label={`Choose ${icon} icon`}
                aria-pressed={def.icon === icon}
                onClick={() => setDef({ ...def, icon })}
                className={cn(
                  "grid min-h-11 place-items-center border",
                  def.icon === icon ? "border-accent bg-accent/10" : "border-line2",
                )}
              >
                <NodeIcon name={icon} className="size-6 text-accent" />
              </button>
            ))}
          </div>
        </fieldset>
        <div className="grid gap-4 sm:grid-cols-3">
          <label className="block">
            <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Effort</span>
            <select
              value={def.effort}
              onChange={(e) => setDef({ ...def, effort: e.target.value })}
              className="min-h-11 w-full border border-line2 bg-bg px-3 font-mono text-[11px] uppercase text-ink"
            >
              {["low", "medium", "high", "xhigh", "max"].map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Minutes</span>
            <Input
              type="number"
              min={10}
              max={480}
              value={def.minutes}
              onChange={(e) => setDef({ ...def, minutes: Number(e.target.value) })}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Executor</span>
            <select
              value={def.executor}
              onChange={(e) => setDef({ ...def, executor: e.target.value })}
              className="min-h-11 w-full border border-line2 bg-bg px-3 font-mono text-[11px] uppercase text-ink"
              title="claude = Claude Code; codex = OpenAI Codex CLI (ADR-0018). The allowlist grows by its own ADR."
            >
              {["claude", "codex"].map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </select>
          </label>
        </div>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
            Model · optional
          </span>
          <Input
            value={def.model}
            onChange={(e) => setDef({ ...def, model: e.target.value })}
            placeholder="executor default"
            title="Exact model id (e.g. claude-fable-5, gpt-5.6-terra). Must belong to the executor's family; empty keeps the scheduler default."
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">
            Output contract
          </span>
          <Input
            value={def.output}
            onChange={(e) => setDef({ ...def, output: e.target.value })}
            placeholder="Severity-ranked findings report"
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block font-mono text-[10px] uppercase text-mut">Role prompt</span>
          <Textarea
            className="min-h-[180px] font-mono text-[12px] leading-relaxed"
            value={def.prompt}
            onChange={(e) => setDef({ ...def, prompt: e.target.value })}
            placeholder="# Node · Security audit&#10;&#10;Audit the run branch for…"
          />
        </label>
        <Button
          variant="accent"
          size="block"
          disabled={!valid || create.isPending}
          onClick={() => create.mutate()}
        >
          <Save className="size-4" /> {create.isPending ? "Creating…" : "Create node"}
        </Button>
        <ErrorText>{create.error instanceof Error ? create.error.message : null}</ErrorText>
      </Card>
    </section>
  );
}

export function NodeLibrary() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { data: catalog } = useFlowCatalog();
  if (id === "new") return <NodeCreator />;
  if (id) return <PromptEditor role={id.replace(/^node-/, "")} />;
  return (
    <section>
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <Kicker>// node library</Kicker>
          <h1 className="mt-2 font-head text-[26px] font-bold">Reusable session roles</h1>
          <p className="mt-1 text-[12.5px] text-mut">
            Inspect the exact instructions, runtime contract, and revision history behind every node
            — and define new roles for your automations.
          </p>
        </div>
        <Button variant="accent" size="md" onClick={() => navigate("/automations/nodes/new")}>
          <Plus className="size-4" /> New node
        </Button>
      </div>
      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(catalog?.nodes ?? []).map((node) => (
          <button
            key={node.id}
            onClick={() => navigate(`/automations/nodes/${node.id}`)}
            className="border border-line bg-surface p-4 text-left transition-colors hover:border-line2 hover:bg-raise"
          >
            <div className="flex items-center gap-2">
              <span className="font-head text-[15px] font-bold">{node.name}</span>
              {node.custom ? (
                <Badge tone="review">operator-defined</Badge>
              ) : (
                <Badge tone={node.promptSource === "custom" ? "accent" : "muted"}>
                  {node.promptSource}
                </Badge>
              )}
              {node.runtimeSource === "custom" && <Badge tone="review">runtime</Badge>}
            </div>
            <p className="mt-2 min-h-10 text-[11.5px] leading-relaxed text-mut">
              {node.description}
            </p>
            <div className="mt-3 flex items-center justify-between border-t border-line pt-3 font-mono text-[9.5px] uppercase text-dim">
              <span className="inline-flex items-center gap-1.5">
                <ExecutorIcon executor={node.executor} className="size-3.5" />
                {node.effort} · {node.minutes}m
              </span>
              <span>{node.promptRevision} →</span>
            </div>
          </button>
        ))}
      </div>
    </section>
  );
}
