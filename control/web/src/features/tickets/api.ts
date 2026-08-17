import { getJSON, postJSON } from "@/shared/api/http";

export interface TicketNote {
  at: string;
  by: string;
  text: string;
}

export type TicketStatus = "open" | "review" | "closed";
export type TicketLane = "" | "hunt" | "improve" | "ops";

export interface Ticket {
  id: string;
  agent: string;
  title: string;
  body?: string;
  status: TicketStatus;
  lane?: TicketLane;
  claim?: string;
  createdBy: string;
  created: string;
  updated: string;
  notes?: TicketNote[];
}

export interface TicketGroup {
  agent: string;
  tickets: Ticket[];
}

export const ticketsApi = {
  tickets: () => getJSON<TicketGroup[]>("/api/tickets"),
  createTicket: (agent: string, title: string, body: string, lane?: TicketLane) =>
    postJSON("/api/ticket", { agent, title, body, ...(lane ? { lane } : {}) }),
  updateTicket: (agent: string, id: string, status: string, note: string) =>
    postJSON("/api/ticket/update", { agent, id, status, note }),
};
