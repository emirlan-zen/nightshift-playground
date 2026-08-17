import { getJSON, postJSON, queryValue as q } from "@/shared/api/http";

export interface SessionState {
  instance: string;
  slot?: string;
  active: boolean;
  ttl_until?: number;
  // ADR-0018 executor identity, added to the live-session payload by the Go
  // control plane (defaults to "claude" server-side; optional here so the UI
  // never blocks on the field). Consumed by the executor icon on session cards.
  executor?: string;
}

export interface ServerState {
  company: string;
  active: boolean;
  ttl_until?: number;
  sessions: SessionState[];
}

export const sessionsApi = {
  status: () => getJSON<ServerState[]>("/api/status"),
  start: (company: string) => postJSON(`/api/start?c=${q(company)}`),
  stop: (company: string) => postJSON(`/api/stop?c=${q(company)}`),
  sessionStart: (company: string, slot?: string) =>
    postJSON(`/api/session/start?c=${q(company)}${slot ? `&slot=${q(slot)}` : ""}`),
  sessionStop: (company: string, instance: string) =>
    postJSON(`/api/session/stop?c=${q(company)}&instance=${q(instance)}`),
};
