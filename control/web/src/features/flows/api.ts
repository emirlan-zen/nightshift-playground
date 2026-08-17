import { deleteJSON, getJSON, postJSON, putJSON, queryValue as q } from "@/shared/api/http";

export type FlowStatus = "queued" | "running" | "complete" | "blocked" | "deadline" | "stopped";

export interface FlowNodeDefinition {
  id: string;
  name: string;
  description: string;
  effort: string;
  minutes: number;
  output: string;
  executor?: string;
  model?: string; // pinned model id; absent = executor default
  icon?: string;
  custom?: boolean;
  promptId: string;
  prompt: string;
  defaultPrompt: string;
  promptSource: "built-in" | "custom";
  promptRevision: string;
  runtimeSource: "built-in" | "custom";
  runtimeRevision: string;
  defaultEffort: string;
  defaultMinutes: number;
}

export interface RouteEdge {
  node: string;
  verdict: "needs-work";
  append: string[];
}

/** Effort/minutes/executor/model pins — the shape of a node-runtime override,
 * reused per stage member by `memberRuntime` (ADR-0023). */
export interface NodeRuntimeOverride {
  effort?: string;
  minutes?: number;
  executor?: string;
  model?: string;
}

/** ADR-0023 emit-nodes: which node may produce runtime children, how many, from
 * which roles, and which role fans the group back in. Emission by a node not
 * declared here is refused. */
export interface EmitterSpec {
  node: string;
  max: number;
  roles: string[];
  fanIn: string;
}

/** One judge score about one subject, as carried on a ledger node. */
export interface ScoreView {
  dimension: string;
  /** null = the judge recorded `unknown` rather than guessing. */
  score: number | null;
  max: number;
  rationale?: string;
}

/** Aggregated scores for one (node role, dimension, prompt revision, and the
 * executor/model that produced the judged work). The runtime is part of the
 * IDENTITY: "same prompt on claude vs codex" is the harness question, so the two
 * engines are two trends, never one blurred average. */
export interface ScoreGroup {
  role: string;
  dimension: string;
  promptRev: string;
  /** Executor/model of the SUBJECT (absent when the judged thing is free text). */
  executor?: string;
  model?: string;
  /** Mean of the non-null scores, on `max`'s scale; null when every score is unknown. */
  avg: number | null;
  /** The same mean as a percentage — scale-free, so a judge that switched from /5
   * to /10 does not read as a regression. */
  pct: number | null;
  n: number;
  max: number;
  latestRationales?: string[];
}

/** One persisted score row (returned for a single run). */
export interface ScoreRow {
  runId: string;
  judgeNode: string;
  subject: string;
  dimension: string;
  score: number | null;
  max: number;
  rationale?: string;
  judgeExecutor?: string;
  judgeModel?: string;
  subjectExecutor?: string;
  subjectModel?: string;
  createdAt: number;
}

export interface ScoresResponse {
  /** Newest prompt revision first. */
  groups: ScoreGroup[];
  /** Only populated for a single run; empty for an automation query. */
  rows: ScoreRow[];
}

/** ADR-0019: a template with a schedule is a recurring automation — the
 * scheduler instantiates a run daily at `time` (Asia/Bishkek), overlap-safe. */
export interface TemplateSchedule {
  agent: string;
  repo: string;
  goal: string;
  guidance?: string;
  acceptance?: string[];
  time: string; // "HH:MM"
  deadlineMinutes?: number;
  maxConcurrent?: number;
  fastHandoff?: boolean;
}

export interface FlowTemplate {
  id: string;
  name: string;
  description: string;
  /** Absent on a stage-only automation (compare-harness, explore-attempts): the
   * server sends `nodes: null` there, so read the sequence via `rolesOf`. */
  nodes?: string[];
  /** Stage members are `role` or `role#N` (N ∈ 2..4) — two members of one role
   * in the same stage, each with its own runtime pin (ADR-0023). */
  stages?: string[][];
  edges?: RouteEdge[];
  emitters?: EmitterSpec[];
  /** Keyed by the exact stage member ("attempt", "attempt#2"). */
  memberRuntime?: Record<string, NodeRuntimeOverride>;
  schedule?: TemplateSchedule;
  builtin?: boolean;
  customized?: boolean;
  revision?: string;
  updatedAt?: number;
}

export type Verdict = "ok" | "needs-work" | "blocked" | "complete";

export interface NewNodeInput {
  id: string;
  name: string;
  description: string;
  effort: string;
  minutes: number;
  output: string;
  prompt: string;
  executor?: string; // claude (default) | codex, ADR-0018
  model?: string; // optional pinned model id (must match the executor's family)
  icon: string;
}

export interface ChangeProposal {
  name: string;
  valid: boolean;
  error?: string;
  type: "node-prompt" | "node-def" | "template";
  target?: string;
  why: string;
  body?: string;
  def?: Partial<FlowNodeDefinition>;
  prompt?: string;
  template?: FlowTemplate;
  current?: string;
}

export interface LedgerNode {
  id: string;
  role: string;
  round?: number;
  verdict?: Verdict | "";
  state: string;
  effort?: string;
  minutes?: number;
  executor?: string;
  promptRevision?: string;
  startedAt?: string;
  durationMin?: number;
  parallel?: boolean;
  queuedAt?: string;
  waitMin?: number;
  /** Judge scores about this node, absent when nothing scored it (ADR-0023). */
  scores?: ScoreView[];
}

export interface LedgerRun {
  id: string;
  agent: string;
  repo: string;
  template?: string;
  automationRevision?: string;
  status: FlowStatus;
  rounds: number;
  maxConcurrent?: number;
  created: string;
  updated: string;
  nodes: LedgerNode[];
}

export interface FlowCatalog {
  templates: FlowTemplate[];
  nodes: FlowNodeDefinition[];
}

export interface RepoOption {
  agent: string;
  path: string;
  name: string;
}

export interface FlowNode {
  id: string;
  role: string;
  jobId: string;
  round?: number;
  afterId?: string;
  afterIds?: string[];
  stage?: number;
  worktree?: string;
  branch?: string;
  verdictSeen?: string;
  verdict?: Verdict | "";
  /** Node id of the emitter that produced this node at runtime; absent when the
   * node comes from the automation's own stages (ADR-0023). */
  emittedBy?: string;
  name: string;
  description: string;
  output: string;
  effort: string;
  minutes: number;
  executor?: string; // claude | codex (ADR-0018)
  model?: string; // pinned model id; absent = executor default
  promptId: string;
  promptRevision: string;
  startedAt?: string;
  state: "waiting" | "queued" | "running" | "delivered" | "skipped" | "stopped";
  reportId?: string;
}

export interface Flow {
  id: string;
  agent: string;
  repo: string;
  goal: string;
  /** Normalized to [] server-side, but old/raw payloads may carry null. */
  acceptance: string[] | null;
  guidance?: string;
  fastHandoff?: boolean;
  maxConcurrent?: number;
  template: string;
  automationRevision?: string;
  nodeRoles: string[];
  stages?: string[][];
  edges?: RouteEdge[];
  /** Pinned at create, like stages/edges — a later template edit can never widen
   * a live run's emission permissions (ADR-0016 pinning, ADR-0023). */
  emitters?: EmitterSpec[];
  memberRuntime?: Record<string, NodeRuntimeOverride>;
  deadline?: string;
  created: string;
  updated: string;
  status: FlowStatus;
  batch: string;
  branch: string;
  base: string;
  sourceRepo?: string;
  worktree?: string;
  worktreeState: "active" | "cleaned" | "retained";
  cleanupMessage?: string;
  round: number;
  nodeViews: FlowNode[];
}

export interface CreateFlowInput {
  agent: string;
  repo: string;
  goal: string;
  acceptance: string[];
  guidance?: string;
  fastHandoff?: boolean;
  maxConcurrent?: number;
  template: string;
  nodes?: string[];
  stages?: string[][];
  edges?: RouteEdge[];
  deadline?: string;
  base?: string;
}

export const flowsApi = {
  flowCatalog: () => getJSON<FlowCatalog>("/api/flow-catalog"),
  repos: () => getJSON<RepoOption[]>("/api/repos"),
  flows: () => getJSON<Flow[]>("/api/flows"),
  flow: (id: string) => getJSON<Flow>(`/api/flows/${q(id)}`),
  createFlow: (input: CreateFlowInput) => postJSON<Flow>("/api/flows", input),
  flowTemplates: () => getJSON<FlowTemplate[]>("/api/flow-templates"),
  saveFlowTemplate: (id: string, template: FlowTemplate) =>
    putJSON<FlowTemplate>(`/api/flow-templates/${q(id)}`, template),
  resetFlowTemplate: (id: string) => deleteJSON(`/api/flow-templates/${q(id)}`),
  saveNodeRuntime: (
    id: string,
    effort: string,
    minutes: number,
    executor?: string,
    model?: string,
  ) =>
    putJSON<{ id: string; effort: string; minutes: number; source: string }>(
      `/api/node-runtime/${q(id)}`,
      { effort, minutes, ...(executor ? { executor } : {}), ...(model ? { model } : {}) },
    ),
  resetNodeRuntime: (id: string) => deleteJSON(`/api/node-runtime/${q(id)}`),
  setFlowDeadline: (id: string, deadline?: string) =>
    putJSON<Flow>(`/api/flows/${q(id)}/deadline`, { deadline: deadline ?? "" }),
  setFlowGuidance: (id: string, guidance: string) =>
    putJSON<Flow>(`/api/flows/${q(id)}/guidance`, { guidance }),
  stopFlow: (id: string) => postJSON<Flow>(`/api/flows/${q(id)}/stop`),
  cleanupFlow: (id: string) => postJSON<Flow>(`/api/flows/${q(id)}/cleanup`),
  createNode: (input: NewNodeInput) => postJSON<FlowNodeDefinition[]>("/api/nodes", input),
  updateNode: (id: string, input: Omit<NewNodeInput, "prompt">) =>
    putJSON<FlowNodeDefinition[]>(`/api/nodes/${q(id)}`, input),
  deleteNode: (id: string) => deleteJSON(`/api/nodes/${q(id)}`),
  ledger: () => getJSON<LedgerRun[]>("/api/ledger"),
  // Scores are read either for one run or for one automation — never both, which
  // the server rejects, so the client exposes them as two calls (ADR-0023).
  runScores: (id: string) => getJSON<ScoresResponse>(`/api/scores?run=${q(id)}`),
  automationScores: (id: string) => getJSON<ScoresResponse>(`/api/scores?automation=${q(id)}`),
  changeProposals: () => getJSON<ChangeProposal[]>("/api/proposals"),
  applyChangeProposal: (name: string) => postJSON(`/api/proposals/${q(name)}/apply`),
  dismissChangeProposal: (name: string) => deleteJSON(`/api/proposals/${q(name)}`),
};
