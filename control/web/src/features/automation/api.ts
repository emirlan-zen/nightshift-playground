import {
  deleteJSON,
  getJSON,
  getText,
  postJSON,
  putJSON,
  queryValue as q,
} from "@/shared/api/http";

export interface Job {
  id: string;
  agent: string;
  prompt: string;
  at: string;
  kind: string;
  model?: string;
  effort?: string;
  executor?: string; // claude (default) | codex, ADR-0018
  label?: string;
  minutes?: number;
  created: string;
  started: boolean;
  startedAt?: string;
  runState?: string;
  after?: string[];
  gated?: boolean;
  skipped?: boolean;
  flowId?: string;
  nodeId?: string;
  workdir?: string;
  finishBy?: string;
}

export type SweepMap = Record<string, boolean>;

export interface Report {
  id: string;
  mtime: number;
  banner?: boolean;
  headline?: string;
  tone?: string;
  wave?: string;
  stats?: string;
}

export interface PipelineWave {
  name: string;
  time: string;
  after?: string[];
  effort?: string;
  executor?: string; // "codex" when the wave runs on Codex (ADR-0018)
  minutes: number;
  perTicket?: boolean;
  noTickets?: boolean;
}

export interface Pipeline {
  agent: string;
  source: "builtin" | "profile" | "legacy";
  profile?: string;
  waves: PipelineWave[];
}

export interface WorkerSplit {
  hunt: number;
  improve: number;
}

export interface ProfileWave {
  name: string;
  time?: string;
  after?: string[];
  prompt: string;
  model?: string;
  effort?: string;
  executor?: string; // claude (default) | codex, ADR-0018
  minutes: number;
  perTicket?: boolean;
  noTickets?: boolean;
}

export interface Profile {
  name: string;
  deadline?: string;
  workers?: WorkerSplit;
  waves: ProfileWave[];
  why?: string;
}

export interface ProfileSummary {
  name: string;
  active: boolean;
  effective?: boolean;
  waves: number;
  deadline?: string;
  workers?: WorkerSplit;
  hasFanout?: boolean;
}

export interface ProfilesDoc {
  active: string;
  effective: string;
  profiles: ProfileSummary[];
  builtin: Profile;
}

export interface Proposal {
  name: string;
  why?: string;
  valid: boolean;
  error?: string;
  profile?: Profile;
}

export const automationApi = {
  sweeps: () => getJSON<SweepMap>("/api/sweeps"),
  setSweep: (company: string, on: boolean) =>
    postJSON(`/api/sweep?c=${q(company)}&on=${on ? 1 : 0}`),
  jobs: (company: string) => getJSON<Job[]>(`/api/jobs?c=${q(company)}`),
  queueJob: (
    agent: string,
    prompt: string,
    at: string,
    options?: { effort?: string; executor?: string; minutes?: number },
  ) =>
    postJSON("/api/job", {
      agent,
      prompt,
      at,
      ...(options?.effort ? { effort: options.effort } : {}),
      ...(options?.executor ? { executor: options.executor } : {}),
      ...(options?.minutes ? { minutes: options.minutes } : {}),
    }),
  cancelJob: (company: string, id: string) =>
    postJSON(`/api/job/cancel?c=${q(company)}&id=${q(id)}`),
  stopRun: (company: string, id: string) => postJSON(`/api/run/stop?c=${q(company)}&id=${q(id)}`),
  pipeline: (agent?: string) => getJSON<Pipeline>(`/api/pipeline${agent ? `?c=${q(agent)}` : ""}`),
  profiles: (agent?: string) =>
    getJSON<ProfilesDoc>(`/api/profiles${agent ? `?c=${q(agent)}` : ""}`),
  profile: (name: string) => getJSON<Profile>(`/api/profiles/${q(name)}`),
  saveProfile: (name: string, profile: Profile) => putJSON(`/api/profiles/${q(name)}`, profile),
  deleteProfile: (name: string) => deleteJSON(`/api/profiles/${q(name)}`),
  activateProfile: (name: string, agent?: string) =>
    postJSON("/api/profiles/active", { name, ...(agent ? { agent } : {}) }),
  runProfile: (name: string, agent?: string) =>
    postJSON(`/api/profiles/${q(name)}/run${agent ? `?c=${q(agent)}` : ""}`),
  proposals: () => getJSON<Proposal[]>("/api/profiles/proposals"),
  applyProposal: (name: string) => postJSON(`/api/profiles/proposals/${q(name)}/apply`),
  dismissProposal: (name: string) => deleteJSON(`/api/profiles/proposals/${q(name)}`),
  reports: (company: string) => getJSON<Report[]>(`/api/reports?c=${q(company)}`),
  report: (company: string, id: string) => getText(`/api/report?c=${q(company)}&id=${q(id)}`),
  reportBanner: (company: string, id: string) => `/api/report/banner?c=${q(company)}&id=${q(id)}`,
};
