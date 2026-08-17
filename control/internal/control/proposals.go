package control

// Widened proposal inbox (ADR-0017). The system retro may WRITE
// ~/.nightshift/proposals/<name>.json describing a definition change — a node
// prompt edit, a new/updated custom node definition, or a flow template — with
// a required `why`. The scheduler never reads this directory; only the
// operator's Apply promotes one, and Apply routes through the SAME versioned,
// validated save paths the operator's own edits use (savePromptBody /
// saveCustomNodeDef / saveFlowTemplate), so prompt-history records the change
// and revert stays one tap. Proposals cover definitions only — never the
// instantiation of work. Junk is visible with its error and dismissible, never
// silently applied.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type changeProposal struct {
	Type     string          `json:"type"` // node-prompt | node-def | template
	Target   string          `json:"target,omitempty"`
	Why      string          `json:"why"`
	Body     string          `json:"body,omitempty"`     // node-prompt: the proposed prompt
	Def      *nodeDefinition `json:"def,omitempty"`      // node-def: the proposed definition
	Prompt   string          `json:"prompt,omitempty"`   // node-def: prompt for a NEW role
	Template *flowTemplate   `json:"template,omitempty"` // template: the proposed template
}

type changeProposalView struct {
	Name  string `json:"name"`
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
	changeProposal
	Current string `json:"current,omitempty"` // what the target looks like today (diff basis)
}

func proposalsDir() string            { return filepath.Join(nsDir(), "proposals") }
func proposalPath(name string) string { return filepath.Join(proposalsDir(), name+".json") }

// validateChangeProposal checks a proposal exactly as Apply would, so the inbox
// shows problems before the operator taps.
func validateChangeProposal(p changeProposal) error {
	if strings.TrimSpace(p.Why) == "" {
		return errors.New("proposal wants a why")
	}
	switch p.Type {
	case "node-prompt":
		if _, ok := nodeDefinitionByID(p.Target); !ok {
			return errors.New("unknown node " + p.Target)
		}
		if strings.TrimSpace(p.Body) == "" || len(p.Body) > promptBodyMax {
			return errors.New("prompt body is empty or too large")
		}
		return nil
	case "node-def":
		if p.Def == nil {
			return errors.New("node-def proposal wants a def")
		}
		if err := validateNodeDefinition(*p.Def); err != nil {
			return err
		}
		if !isCustomNode(p.Def.ID) && strings.TrimSpace(p.Prompt) == "" {
			return errors.New("a new node wants a prompt")
		}
		return nil
	case "template":
		if p.Template == nil {
			return errors.New("template proposal wants a template")
		}
		return validateFlowTemplate(*p.Template)
	}
	return errors.New("type must be node-prompt|node-def|template")
}

// proposalCurrent returns today's state of the proposal's target so the UI can
// render a real diff.
func proposalCurrent(p changeProposal) string {
	switch p.Type {
	case "node-prompt":
		return nodePrompt(p.Target)
	case "node-def":
		if p.Def == nil {
			return ""
		}
		if d, ok := loadCustomNodeDefs()[p.Def.ID]; ok {
			b, _ := json.MarshalIndent(d, "", "  ")
			return string(b)
		}
		return ""
	case "template":
		if p.Template == nil {
			return ""
		}
		if t, ok := templateByID(p.Template.ID); ok {
			b, _ := json.MarshalIndent(t, "", "  ")
			return string(b)
		}
	}
	return ""
}

func applyChangeProposal(p changeProposal) error {
	if err := validateChangeProposal(p); err != nil {
		return err
	}
	switch p.Type {
	case "node-prompt":
		_, err := savePromptBody("node-"+p.Target, p.Body)
		return err
	case "node-def":
		newNode := !isCustomNode(p.Def.ID)
		if err := saveCustomNodeDef(*p.Def); err != nil {
			return err
		}
		if strings.TrimSpace(p.Prompt) != "" {
			if _, err := savePromptBody("node-"+p.Def.ID, p.Prompt); err != nil {
				// Don't leave a promptless new role behind — mirror
				// handleNodeCreate's rollback. (An edit to an existing node keeps
				// its prior def; only a brand-new node is rolled back.)
				if newNode {
					_ = os.Remove(nodeDefPath(p.Def.ID))
				}
				return err
			}
		}
		return nil
	case "template":
		return saveFlowTemplate(*p.Template)
	}
	return errors.New("unreachable proposal type")
}

func handleChangeProposals(w http.ResponseWriter, _ *http.Request) {
	nsMu.Lock()
	defer nsMu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(proposalsDir(), "*.json"))
	sort.Strings(matches)
	out := []changeProposalView{}
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".json")
		if !profileNameRe.MatchString(name) {
			continue
		}
		v := changeProposalView{Name: name}
		b, err := os.ReadFile(m)
		if err != nil {
			v.Error = err.Error()
			out = append(out, v)
			continue
		}
		if err := json.Unmarshal(b, &v.changeProposal); err != nil {
			v.Error = "bad json: " + err.Error()
			out = append(out, v)
			continue
		}
		if err := validateChangeProposal(v.changeProposal); err != nil {
			v.Error = err.Error()
		} else {
			v.Valid = true
			v.Current = proposalCurrent(v.changeProposal)
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

func handleChangeProposalApply(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !profileNameRe.MatchString(name) {
		http.Error(w, "invalid proposal name", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	b, err := os.ReadFile(proposalPath(name))
	if err != nil {
		http.Error(w, "no such proposal", http.StatusNotFound)
		return
	}
	var p changeProposal
	if err := json.Unmarshal(b, &p); err != nil {
		http.Error(w, "proposal invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := applyChangeProposal(p); err != nil { // re-validate at apply time
		http.Error(w, "proposal invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	_ = os.Remove(proposalPath(name)) // consumed
	writeJSON(w, map[string]string{"applied": name})
}

func handleChangeProposalDismiss(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !profileNameRe.MatchString(name) {
		http.Error(w, "invalid proposal name", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if err := os.Remove(proposalPath(name)); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "no such proposal", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"dismissed": name})
}
