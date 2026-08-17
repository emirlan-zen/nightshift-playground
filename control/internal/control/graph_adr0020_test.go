package control

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestADR0020BuiltinIconsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for id, d := range nodeDefinitions {
		if d.Icon == "" || seen[d.Icon] {
			t.Fatalf("node %s icon %q is empty or duplicated", id, d.Icon)
		}
		seen[d.Icon] = true
	}
	good := nodeDefinition{ID: "my-node", Name: "Mine", Effort: "high", Minutes: 60, Icon: "scribe"}
	if err := validateNodeDefinition(good); err != nil {
		t.Fatal(err)
	}
	good.Icon = "unknown"
	if err := validateNodeDefinition(good); err == nil {
		t.Fatal("unknown icon accepted")
	}
}

func TestADR0020TemplateCanonicalizationAndIssues(t *testing.T) {
	flowEnv(t)
	tpl := flowTemplate{ID: "graph-test", Name: "Graph", Nodes: []string{"plan", "review"}}
	if err := saveFlowTemplate(tpl); err != nil {
		t.Fatal(err)
	}
	got, err := loadCustomFlowTemplate(flowTemplatePath(tpl.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Stages) != 2 || strings.Join(got.Nodes, ",") != "plan,review" {
		t.Fatalf("not canonical: %+v", got)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/flow-templates/graph-test", strings.NewReader(`{"name":"Bad","stages":[["plan","review","amend","validate","preview"]]}`))
	req.SetPathValue("id", "graph-test")
	handleFlowTemplatePut(rr, req)
	if rr.Code != 422 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body graphValidationError
	if json.Unmarshal(rr.Body.Bytes(), &body) != nil || len(body.Issues) == 0 || body.Issues[0].Path != "stages.0" {
		t.Fatalf("issues=%s", rr.Body.String())
	}
}

// The editor must reject an oversized graph on save (ADR-0020), rather than
// letting it surface at mint time (validateStages/validateEdges) off the graph.
// These caps mirror the lifetime node budget the run enforces.
func TestADR0020TemplateSizeCaps(t *testing.T) {
	flowEnv(t)
	code := func(err error) string {
		var g graphValidationError
		if !errors.As(err, &g) || len(g.Issues) == 0 {
			t.Fatalf("want a graph issue, got %v", err)
		}
		return g.Issues[0].Code
	}
	repeat := func(n int, role string) [][]string {
		out := make([][]string, n)
		for i := range out {
			out[i] = []string{role}
		}
		return out
	}
	// 17 single-node stages exceed the 16-session lifetime budget.
	if got := code(validateFlowTemplate(flowTemplate{ID: "too-big", Name: "Too big", Stages: repeat(17, "plan")})); got != "too-many-nodes" {
		t.Fatalf("node cap: got %q", got)
	}
	// 17 declared needs-work routes exceed the edge budget.
	edges := make([]routeEdge, 17)
	for i := range edges {
		edges[i] = routeEdge{Node: "plan", Verdict: "needs-work", Append: []string{"amend"}}
	}
	if got := code(validateFlowTemplate(flowTemplate{ID: "many-edges", Name: "Many edges", Stages: [][]string{{"plan"}}, Edges: edges})); got != "too-many-edges" {
		t.Fatalf("edge cap: got %q", got)
	}
	// A single route appending 17 nodes exceeds the append budget.
	long := make([]string, 17)
	for i := range long {
		long[i] = "amend"
	}
	if got := code(validateFlowTemplate(flowTemplate{ID: "long-route", Name: "Long route", Stages: [][]string{{"plan"}}, Edges: []routeEdge{{Node: "plan", Verdict: "needs-work", Append: long}}})); got != "route-too-long" {
		t.Fatalf("append cap: got %q", got)
	}
	// A template exactly at the 16-node cap stays valid.
	if err := validateFlowTemplate(flowTemplate{ID: "at-cap", Name: "At cap", Stages: repeat(16, "plan")}); err != nil {
		t.Fatalf("16 nodes rejected: %v", err)
	}
}
