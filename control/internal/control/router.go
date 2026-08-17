package control

import (
	"log/slog"
	"net/http"

	"nightshift/control/internal/access"
	"nightshift/control/internal/focus"
	"nightshift/control/internal/machine"
	"nightshift/control/internal/session"
	"nightshift/control/internal/transport/httpapi"
)

type routeHandlers struct {
	sessions *session.Handler
	focus    *focus.Handler
	machine  *machine.Handler
}

type authenticate func(http.HandlerFunc) http.HandlerFunc

func newRouter(logger *slog.Logger, accessVerifier *access.Verifier, handlers routeHandlers) http.Handler {
	mux := http.NewServeMux()
	auth := accessVerifier.Handler
	registerSessionRoutes(mux, auth, handlers.sessions)
	registerAutomationRoutes(mux, auth)
	registerFlowRoutes(mux, auth)
	registerTicketRoutes(mux, auth)
	registerSystemRoutes(mux, auth, handlers)
	return httpapi.AccessLog(logger.With("component", "http"), mux)
}

func registerSessionRoutes(mux *http.ServeMux, auth authenticate, sessions *session.Handler) {
	mux.HandleFunc("GET /api/status", auth(sessions.Status))
	mux.HandleFunc("POST /api/start", auth(sessions.Action("start")))
	mux.HandleFunc("POST /api/stop", auth(sessions.Action("stop")))
	mux.HandleFunc("POST /api/session/start", auth(sessions.Start))
	mux.HandleFunc("POST /api/session/stop", auth(sessions.Stop))
}

func registerAutomationRoutes(mux *http.ServeMux, auth authenticate) {
	mux.HandleFunc("GET /api/jobs", auth(handleJobs))
	mux.HandleFunc("POST /api/job", auth(handleJobCreate))
	mux.HandleFunc("POST /api/job/cancel", auth(handleJobCancel))
	mux.HandleFunc("POST /api/run/stop", auth(handleRunStop))
	mux.HandleFunc("GET /api/sweeps", auth(handleSweeps))
	mux.HandleFunc("POST /api/sweep", auth(handleSweepToggle))
	mux.HandleFunc("GET /api/pipeline", auth(handlePipeline))
	mux.HandleFunc("GET /api/profiles", auth(handleProfiles))
	mux.HandleFunc("GET /api/profiles/proposals", auth(handleProposals))
	mux.HandleFunc("POST /api/profiles/proposals/{name}/apply", auth(handleProposalApply))
	mux.HandleFunc("DELETE /api/profiles/proposals/{name}", auth(handleProposalDismiss))
	mux.HandleFunc("POST /api/profiles/active", auth(handleProfileActive))
	mux.HandleFunc("POST /api/profiles/{name}/run", auth(handleProfileRun))
	mux.HandleFunc("GET /api/profiles/{name}", auth(handleProfileGet))
	mux.HandleFunc("PUT /api/profiles/{name}", auth(handleProfilePut))
	mux.HandleFunc("DELETE /api/profiles/{name}", auth(handleProfileDelete))
	mux.HandleFunc("GET /api/reports", auth(handleReports))
	mux.HandleFunc("GET /api/report", auth(handleReport))
	mux.HandleFunc("GET /api/report/banner", auth(handleReportBanner))
}

func registerFlowRoutes(mux *http.ServeMux, auth authenticate) {
	mux.HandleFunc("GET /api/flow-catalog", auth(handleFlowCatalog))
	mux.HandleFunc("GET /api/flow-templates", auth(handleFlowTemplates))
	mux.HandleFunc("PUT /api/flow-templates/{id}", auth(handleFlowTemplatePut))
	mux.HandleFunc("DELETE /api/flow-templates/{id}", auth(handleFlowTemplateDelete))
	mux.HandleFunc("PUT /api/node-runtime/{id}", auth(handleNodeRuntimePut))
	mux.HandleFunc("DELETE /api/node-runtime/{id}", auth(handleNodeRuntimeDelete))
	mux.HandleFunc("POST /api/nodes", auth(handleNodeCreate))
	mux.HandleFunc("PUT /api/nodes/{id}", auth(handleNodePut))
	mux.HandleFunc("DELETE /api/nodes/{id}", auth(handleNodeDelete))
	mux.HandleFunc("GET /api/ledger", auth(handleLedger))
	mux.HandleFunc("GET /api/scores", auth(handleScores))
	mux.HandleFunc("GET /api/proposals", auth(handleChangeProposals))
	mux.HandleFunc("POST /api/proposals/{name}/apply", auth(handleChangeProposalApply))
	mux.HandleFunc("DELETE /api/proposals/{name}", auth(handleChangeProposalDismiss))
	mux.HandleFunc("GET /api/repos", auth(handleRepos))
	mux.HandleFunc("GET /api/flows", auth(handleFlows))
	mux.HandleFunc("POST /api/flows", auth(handleFlowCreate))
	mux.HandleFunc("GET /api/flows/{id}", auth(handleFlowGet))
	mux.HandleFunc("PUT /api/flows/{id}/deadline", auth(handleFlowDeadline))
	mux.HandleFunc("PUT /api/flows/{id}/guidance", auth(handleFlowGuidance))
	mux.HandleFunc("POST /api/flows/{id}/stop", auth(handleFlowStop))
	mux.HandleFunc("POST /api/flows/{id}/cleanup", auth(handleFlowCleanup))
}

func registerTicketRoutes(mux *http.ServeMux, auth authenticate) {
	mux.HandleFunc("GET /api/tickets", auth(handleTickets))
	mux.HandleFunc("POST /api/ticket", auth(handleTicketCreate))
	mux.HandleFunc("POST /api/ticket/update", auth(handleTicketUpdate))
}

func registerSystemRoutes(mux *http.ServeMux, auth authenticate, handlers routeHandlers) {
	mux.HandleFunc("GET /", auth(spaHandler()))
	mux.HandleFunc("GET /api/health", auth(handleHealth))
	mux.HandleFunc("GET /api/obs", auth(handleObs))
	mux.HandleFunc("GET /api/vitals", auth(handlers.machine.Get))
	mux.HandleFunc("GET /api/prompts", auth(handlePrompts))
	mux.HandleFunc("GET /api/prompt", auth(handlePrompt))
	mux.HandleFunc("GET /api/prompt-document", auth(handlePromptDocument))
	mux.HandleFunc("PUT /api/prompt-document", auth(handlePromptDocumentPut))
	mux.HandleFunc("POST /api/prompt-document/restore", auth(handlePromptRestore))
	mux.HandleFunc("GET /api/focus", auth(handlers.focus.Get))
	mux.HandleFunc("PUT /api/focus/{id}", auth(handlers.focus.Put))
	mux.HandleFunc("GET /api/ideas", auth(handlers.focus.Ideas))
	mux.HandleFunc("GET /api/idea", auth(handlers.focus.Idea))
	mux.HandleFunc("POST /api/ideas/{id}/promote", auth(handlers.focus.Promote))
	mux.HandleFunc("GET /api/usage", auth(handleUsage))
}
