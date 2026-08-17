package control

import (
	"os"
	"path/filepath"
	"testing"
)

func workbenchEnv(t *testing.T) {
	t.Helper()
	home = t.TempDir()
	if err := os.MkdirAll(nodesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for id := range nodeDefinitions {
		if err := os.WriteFile(filepath.Join(nodesDir(), id+".md"), []byte(defaultNodeFile(id)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The built-in prompt (Go) and the file install.sh seeds onto the box must be
// the same bytes: the workbench's built-in/custom badge and "Revert to
// built-in" compare against defaultNodeFile. A drift here makes every node
// read "custom" on a fresh box and turns Revert into a silent prompt swap.
func TestDefaultNodePromptsMatchShippedFiles(t *testing.T) {
	for id := range nodeDefinitions {
		path := filepath.Join("..", "..", "..", "files", "nightrun", "nodes", id+".md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("node %s: %v", id, err)
		}
		if string(b) != defaultNodeFile(id) {
			t.Errorf("node %s: defaultNodePrompts differs from %s — keep them byte-identical", id, path)
		}
	}
}

func TestPromptDocumentSaveVersionsAndRestore(t *testing.T) {
	workbenchEnv(t)
	id := "node-review"
	original := defaultNodeFile("review")
	custom := "# Node · Review\n\nChallenge the diff and name every regression.\n"

	doc, err := savePromptBody(id, custom)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Body != custom || doc.Source != "custom" || len(doc.Versions) != 1 {
		t.Fatalf("saved doc = %+v", doc)
	}
	version := doc.Versions[0].ID
	b, err := os.ReadFile(filepath.Join(promptHistoryDir(id), version+".md"))
	if err != nil || string(b) != original {
		t.Fatalf("history body=%q err=%v", b, err)
	}

	restored, err := savePromptBody(id, string(b))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Source != "built-in" || restored.Body != original || len(restored.Versions) != 2 {
		t.Fatalf("restored doc = %+v", restored)
	}
}

func TestPromptDocumentRejectsNonNodeWrites(t *testing.T) {
	workbenchEnv(t)
	if _, err := savePromptBody("global", "replace global rules"); err == nil {
		t.Fatal("expected global prompt write to be rejected")
	}
	if _, err := savePromptBody("node-nope", "x"); err == nil {
		t.Fatal("expected unknown node write to be rejected")
	}
}

func TestEffectiveFlowTemplatesOverrideAndReset(t *testing.T) {
	workbenchEnv(t)
	override := flowTemplate{
		ID: "full-delivery", Name: "Full delivery · secure", Description: "Adds preview evidence.",
		Nodes: []string{"plan", "implement", "review", "preview", "validate"},
	}
	if err := saveFlowTemplate(override); err != nil {
		t.Fatal(err)
	}
	got, ok := templateByID("full-delivery")
	if !ok || got.Name != override.Name || !got.Builtin || !got.Customized || got.Revision == "" {
		t.Fatalf("effective override = %+v", got)
	}
	if err := os.Remove(flowTemplatePath("full-delivery")); err != nil {
		t.Fatal(err)
	}
	got, ok = templateByID("full-delivery")
	if !ok || got.Name != "Full delivery" || got.Customized {
		t.Fatalf("reset template = %+v", got)
	}
}

func TestFlowTemplateValidation(t *testing.T) {
	workbenchEnv(t)
	cases := []flowTemplate{
		{ID: "../escape", Name: "bad", Description: "bad", Nodes: []string{"review"}},
		{ID: "valid", Name: "", Description: "bad", Nodes: []string{"review"}},
		{ID: "valid", Name: "bad node", Description: "bad", Nodes: []string{"unknown"}},
	}
	for _, tc := range cases {
		if err := saveFlowTemplate(tc); err == nil {
			t.Fatalf("template should be rejected: %+v", tc)
		}
	}
}

func TestNodeRuntimeOverrideAndReset(t *testing.T) {
	workbenchEnv(t)
	base := nodeDefinitions["review"]
	if err := saveNodeRuntime("review", nodeRuntimeOverride{Effort: "xhigh", Minutes: 135}); err != nil {
		t.Fatal(err)
	}
	got, source := effectiveNodeDefinition("review")
	if source != "custom" || got.Effort != "xhigh" || got.Minutes != 135 {
		t.Fatalf("override = %+v source=%s", got, source)
	}
	if err := os.Remove(nodeRuntimePath("review")); err != nil {
		t.Fatal(err)
	}
	got, source = effectiveNodeDefinition("review")
	if source != "built-in" || got.Effort != base.Effort || got.Minutes != base.Minutes {
		t.Fatalf("reset = %+v source=%s", got, source)
	}
}

func TestNodeRuntimeRejectsUnsafeValues(t *testing.T) {
	workbenchEnv(t)
	for _, override := range []nodeRuntimeOverride{{Effort: "turbo", Minutes: 90}, {Effort: "high", Minutes: 9}, {Effort: "high", Minutes: 481}} {
		if err := saveNodeRuntime("review", override); err == nil {
			t.Fatalf("override should be rejected: %+v", override)
		}
	}
}
