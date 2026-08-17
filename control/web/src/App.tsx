import { BrowserRouter, Navigate, Route, Routes, useParams } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "@/components/AppShell";
import { Home } from "@/views/Home";
import { Inbox } from "@/views/Inbox";
import { Flows } from "@/features/flows/FlowsPage";
import { Servers } from "@/features/sessions/ServersPage";
import { Tickets } from "@/features/tickets/TicketsPage";
import { Automations, NodeLibrary, TemplateLibrary } from "@/features/automation/AutomationsPage";
import { Pipeline } from "@/features/automation/PipelinePage";
import { ReportPage } from "@/features/automation/ReportPage";
import { System } from "@/features/system/SystemPage";
import { Focus } from "@/features/system/FocusPage";
import { Usage } from "@/features/system/UsagePage";
import { Health } from "@/features/system/HealthPage";
import { Prompts, PromptPage } from "@/features/system/PromptsPage";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: true,
      staleTime: 2000,
    },
  },
});

// The legacy /pipeline/:name deep-link now lives under the Automations surface.
function ProfileRedirect() {
  const { name } = useParams();
  return <Navigate to={`/automations/profiles/${name ?? ""}`} replace />;
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        {/*
          Routes group by the five operator objects (ADR-0016): Overview,
          Sessions, Runs, Automations, Inbox — plus Direction and System.
          Terminology (ADR-0020): the UI says "Run" (an instance) and
          "Automation" (a definition); "flow"/"pipeline" survive only as URL
          aliases. The 2026-07-12 UI audit folded the standalone Night and
          Pipeline pages INTO Runs and Automations — those routes now redirect,
          but never delete a route (old links + bookmarks must keep working).
        */}
        <Routes>
          <Route element={<AppShell />}>
            {/* Overview */}
            <Route index element={<Navigate to="/home" replace />} />
            <Route path="/home" element={<Home />} />

            {/* Runs — the single surface for run instances. /night and its
                scheduler history folded in; /flows/* are pre-ADR-0020 aliases. */}
            <Route path="/runs" element={<Flows />} />
            <Route path="/runs/:id" element={<Flows />} />
            <Route path="/flows" element={<Flows />} />
            <Route path="/flows/:id" element={<Flows />} />
            <Route path="/night" element={<Navigate to="/runs" replace />} />
            <Route path="/night/:agent/:id" element={<ReportPage />} />

            {/* Sessions — live remote-control + run sessions. /servers is a legacy alias. */}
            <Route path="/sessions" element={<Servers />} />
            <Route path="/servers" element={<Servers />} />

            {/* Automations — the single surface for definitions: templates, node
                library, and the folded Schedules section. The profile editor
                keeps its own sub-route; the schedules index redirects here. */}
            <Route path="/automations" element={<Automations />} />
            <Route path="/automations/templates" element={<TemplateLibrary />} />
            <Route path="/automations/templates/:id" element={<TemplateLibrary />} />
            <Route path="/automations/nodes" element={<NodeLibrary />} />
            <Route path="/automations/nodes/:id" element={<NodeLibrary />} />
            <Route path="/automations/profiles" element={<Navigate to="/automations" replace />} />
            <Route path="/automations/profiles/:name" element={<Pipeline />} />
            <Route path="/pipeline" element={<Navigate to="/automations" replace />} />
            <Route path="/pipeline/:name" element={<ProfileRedirect />} />

            {/* Inbox + ticket board */}
            <Route path="/inbox" element={<Inbox />} />
            <Route path="/tickets" element={<Tickets />} />
            <Route path="/tickets/:agent/:id" element={<Tickets />} />

            {/* Direction — operator north-star files */}
            <Route path="/focus" element={<Focus />} />
            <Route path="/focus/:id" element={<Focus />} />

            {/* System — health, usage, prompt viewer */}
            <Route path="/system" element={<System />} />
            <Route path="/usage" element={<Usage />} />
            <Route path="/health" element={<Health />} />
            <Route path="/prompts" element={<Prompts />} />
            <Route path="/prompts/:id" element={<PromptPage />} />

            {/* Legacy alias: the time-oriented Morning view folded into Overview. */}
            <Route path="/morning" element={<Navigate to="/home" replace />} />
            <Route path="*" element={<Navigate to="/home" replace />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
