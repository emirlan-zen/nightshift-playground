import { getJSON, getText, postJSON, putJSON, queryValue as q } from "@/shared/api/http";

export interface PromptFile {
  id: string;
  path: string;
  label: string;
  desc?: string;
  exists: boolean;
  editable?: boolean;
  source?: "built-in" | "custom" | "file";
  revision?: string;
}

export interface PromptGroup {
  title: string;
  files: PromptFile[];
}

export interface PromptsDoc {
  groups: PromptGroup[];
}

export interface PromptVersion {
  id: string;
  at: number;
  size: number;
  revision: string;
}

export interface PromptDocument {
  id: string;
  label: string;
  description?: string;
  path: string;
  body: string;
  defaultBody?: string;
  editable: boolean;
  source: "built-in" | "custom" | "file";
  revision: string;
  updatedAt: number;
  versions: PromptVersion[];
}

export interface UsageSums {
  in: number;
  out: number;
  cacheRead: number;
  cacheCreate: number;
  total: number;
  messages: number;
  costUSD: number;
}

export interface DayUsage extends UsageSums {
  date: string;
}

export interface ModelUsage extends UsageSums {
  model: string;
}

export interface AgentUsage extends UsageSums {
  agent: string;
}

export interface Usage {
  windowDays: number;
  generated: number;
  totals: UsageSums;
  days: DayUsage[];
  models: ModelUsage[];
  agents: AgentUsage[];
}

export interface AuthHealth {
  ok: boolean;
  detail: string;
  checkedAt: number;
}

export interface AuthFailure {
  agent: string;
  id: string;
  at: number;
}

export interface ForgeHealth {
  company: string;
  ok: boolean;
  detail: string;
  checkedAt: number;
}

export interface DepSkip {
  agent: string;
  id: string;
  reason: string;
  at: number;
}

export interface Health {
  auth: AuthHealth;
  // Codex executor auth (ADR-0018): last probe snapshot, refreshed by launcher
  // pre-flights (not a ticker — plan quota). Absent = never probed on this box.
  codex?: AuthHealth;
  recentFailures: AuthFailure[];
  forge?: ForgeHealth[];
  depSkips?: DepSkip[];
}

export interface Vitals {
  generated: number;
  uptimeSec: number;
  load1: number;
  load5: number;
  load15: number;
  cpuCount: number;
  memTotalBytes: number;
  memUsedBytes: number;
  swapTotalBytes: number;
  swapUsedBytes: number;
  diskTotalBytes: number;
  diskUsedBytes: number;
  ok: boolean;
}

export interface FocusFile {
  id: string;
  content: string;
  modifiedAt: number;
}

export interface FocusDoc {
  files: FocusFile[];
}

// Scout's discovery backlog (~/.nightshift/research/ideas/<date>.md), read-only
// here plus a one-tap promote into products.md (the Lane A gate).
export interface Idea {
  id: string;
  title: string;
  modifiedAt: number;
}

export interface IdeaBody extends Idea {
  content: string;
}

export interface IdeaCollection {
  files: Idea[];
}

export interface ObsAlert {
  agent: string;
  runId?: string;
  sessionId?: string;
  kind: string;
  detail: string;
  ts: number;
}

export interface ObsAgentStat {
  agent: string;
  sessions: number;
  turns: number;
  toolCalls: number;
  toolErrors: number;
  lastTs: number;
}

export interface ObsRun {
  agent: string;
  runId: string;
  kind?: string;
  label?: string;
  started: number;
  report: boolean;
  status: string;
}

export interface ObsBudget {
  nightBudgetUSD: number;
  nightSpendUSD: number;
  weekSpendUSD: number;
  topUpsMinted: number;
  // Ratchet visibility (ADR-0012): when the budget last auto-adjusted, and the
  // measured ceiling (spend at the last limit-hit). Absent until the first move.
  lastAdjust?: string;
  limitHitNight?: string;
  spendAtHitUSD?: number;
}

export interface Obs {
  generated: number;
  alerts: ObsAlert[];
  agents: ObsAgentStat[];
  runs: ObsRun[];
  budget: ObsBudget;
}

export const systemApi = {
  prompts: () => getJSON<PromptsDoc>("/api/prompts"),
  prompt: (id: string) => getText(`/api/prompt?id=${q(id)}`),
  promptDocument: (id: string) => getJSON<PromptDocument>(`/api/prompt-document?id=${q(id)}`),
  savePrompt: (id: string, body: string) =>
    putJSON<PromptDocument>(`/api/prompt-document?id=${q(id)}`, { body }),
  restorePrompt: (id: string, version: string) =>
    postJSON<PromptDocument>(`/api/prompt-document/restore?id=${q(id)}&version=${q(version)}`),
  focus: () => getJSON<FocusDoc>("/api/focus"),
  saveFocus: (id: string, content: string) => putJSON(`/api/focus/${q(id)}`, { content }),
  ideas: () => getJSON<IdeaCollection>("/api/ideas"),
  idea: (id: string) => getJSON<IdeaBody>(`/api/idea?id=${q(id)}`),
  promoteIdea: (id: string, note: string) =>
    postJSON<FocusFile>(`/api/ideas/${q(id)}/promote`, { note }),
  usage: () => getJSON<Usage>("/api/usage"),
  health: () => getJSON<Health>("/api/health"),
  obs: () => getJSON<Obs>("/api/obs"),
  vitals: () => getJSON<Vitals>("/api/vitals"),
};
