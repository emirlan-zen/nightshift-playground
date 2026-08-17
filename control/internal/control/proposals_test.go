package control

import (
	"os"
	"strings"
	"testing"
)

// Applying a new-node proposal whose prompt save fails must roll the def back,
// mirroring handleNodeCreate — otherwise a promptless role is left behind.
func TestApplyChangeProposalRollsBackPromptlessNewNode(t *testing.T) {
	flowEnv(t)
	p := changeProposal{
		Type:   "node-def",
		Why:    "retro proposed a new audit node",
		Def:    &nodeDefinition{ID: "prop-node", Name: "Prop", Effort: "high", Minutes: 60},
		Prompt: strings.Repeat("x", promptBodyMax+1), // trips savePromptBody's length guard
	}
	if err := applyChangeProposal(p); err == nil {
		t.Fatal("expected the oversized prompt to fail the apply")
	}
	if _, err := os.Stat(nodeDefPath("prop-node")); !os.IsNotExist(err) {
		t.Fatalf("promptless node def left behind after failed apply: %v", err)
	}
	if isCustomNode("prop-node") {
		t.Fatal("node still registered after failed apply")
	}
}
