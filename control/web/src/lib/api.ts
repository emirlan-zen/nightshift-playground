// Backward-compatible public client. Feature code may import from its own
// feature API directly; this barrel keeps existing views and tests stable.
import { automationApi } from "@/features/automation/api";
import { flowsApi } from "@/features/flows/api";
import { sessionsApi } from "@/features/sessions/api";
import { systemApi } from "@/features/system/api";
import { ticketsApi } from "@/features/tickets/api";

export * from "@/features/automation/api";
export * from "@/features/flows/api";
export * from "@/features/sessions/api";
export * from "@/features/system/api";
export * from "@/features/tickets/api";

export const api = {
  ...sessionsApi,
  ...automationApi,
  ...flowsApi,
  ...ticketsApi,
  ...systemApi,
};
