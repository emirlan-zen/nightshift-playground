package control

// Open node catalog (ADR-0017): operator-defined node roles alongside the Go
// built-ins. A custom definition is validated JSON under
// ~/.nightshift/node-defs/<id>.json — NEW ids only; built-ins keep the existing
// prompt/runtime override machinery as their single customization path, so
// there is exactly one way to shadow each thing. A custom node's prompt lives
// in nodes/<id>.md and rides the same versioned savePromptBody path as the
// built-ins. Agents never write here — they may only propose (proposals.go).

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"nightshift/control/internal/run"
)

// validExecutors is the executor allowlist (ADR-0017 seam), derived from the
// run.Executors() domain vocabulary so the queue's dispatch decisions and this
// validation share ONE source of truth (ADR-0020) — a new engine is added in
// one place. codex landed with ADR-0018 (launcher dispatch, codex-auth-probe,
// credentials via the operator's ChatGPT plan); its obs adapter is an accepted
// gap — codex runs are judged by their report artifact, not transcripts.
var validExecutors = func() map[string]bool {
	m := make(map[string]bool, len(run.Executors()))
	for _, e := range run.Executors() {
		m[string(e)] = true
	}
	return m
}()

var customNodeIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// modelIDRe mirrors the launcher's .model sidecar validation — an id that
// fails this would be silently dropped there, so reject it at save instead.
var modelIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var customNodeIcons = []string{"architect", "scout", "medic", "scribe", "herald", "wrench"}

func nodeDefsDir() string          { return filepath.Join(nsDir(), "node-defs") }
func nodeDefPath(id string) string { return filepath.Join(nodeDefsDir(), id+".json") }
func nodeDefHistoryDir(id string) string {
	return filepath.Join(nsDir(), "node-def-history", id)
}

func effectiveExecutor(d nodeDefinition) string {
	if d.Executor == "" {
		return "claude"
	}
	return d.Executor
}

func validateNodeDefinition(d nodeDefinition) error {
	if !customNodeIDRe.MatchString(d.ID) {
		return errors.New("invalid node id")
	}
	if _, builtin := nodeDefinitions[d.ID]; builtin {
		return errors.New("id shadows a built-in node; edit the built-in's prompt/runtime instead")
	}
	d.Name, d.Description, d.Output = strings.TrimSpace(d.Name), strings.TrimSpace(d.Description), strings.TrimSpace(d.Output)
	if d.Name == "" || len(d.Name) > 60 || len(d.Description) > 300 || len(d.Output) > 200 {
		return errors.New("node name, description, or output is invalid")
	}
	if !validEfforts[d.Effort] {
		return errors.New("effort must be low|medium|high|xhigh|max")
	}
	if d.Minutes < runMinutesFloor || d.Minutes > runMinutesCeil {
		return fmt.Errorf("minutes must be %d-%d", runMinutesFloor, runMinutesCeil)
	}
	if d.Executor != "" && !validExecutors[d.Executor] {
		return errors.New("executor must be claude|codex")
	}
	if d.Model != "" && !modelIDRe.MatchString(d.Model) {
		return errors.New("model must be a plain model id ([A-Za-z0-9._-], max 64)")
	}
	if d.Icon != "" && !slices.Contains(customNodeIcons, d.Icon) {
		return errors.New("icon must be architect|scout|medic|scribe|herald|wrench")
	}
	return nil
}

// loadCustomNodeDefs returns every valid custom definition. Invalid files are
// skipped loudly, never half-applied.
func loadCustomNodeDefs() map[string]nodeDefinition {
	out := map[string]nodeDefinition{}
	matches, _ := filepath.Glob(filepath.Join(nodeDefsDir(), "*.json"))
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var d nodeDefinition
		if json.Unmarshal(b, &d) != nil || d.ID != strings.TrimSuffix(filepath.Base(p), ".json") || validateNodeDefinition(d) != nil {
			logf("node def %s ignored: invalid", p)
			continue
		}
		out[d.ID] = d
	}
	return out
}

// nodeDefinitionByID resolves a role across both catalogs: Go built-ins first,
// then the operator's custom definitions.
func nodeDefinitionByID(id string) (nodeDefinition, bool) {
	if d, ok := nodeDefinitions[id]; ok {
		return d, true
	}
	d, ok := loadCustomNodeDefs()[id]
	if ok && d.Icon == "" {
		sum := sha256.Sum256([]byte(id))
		d.Icon = customNodeIcons[int(sum[0])%len(customNodeIcons)]
	}
	return d, ok
}

func isCustomNode(id string) bool {
	_, builtin := nodeDefinitions[id]
	if builtin {
		return false
	}
	_, ok := loadCustomNodeDefs()[id]
	return ok
}

func saveCustomNodeDef(d nodeDefinition) error {
	if err := validateNodeDefinition(d); err != nil {
		return err
	}
	if err := os.MkdirAll(nodeDefsDir(), 0o755); err != nil {
		return err
	}
	path := nodeDefPath(d.ID)
	if old, err := os.ReadFile(path); err == nil {
		hist := nodeDefHistoryDir(d.ID)
		if err := os.MkdirAll(hist, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(hist, strconv.FormatInt(time.Now().UnixNano(), 10)+".json"), old, 0o644)
		}
	}
	b, _ := json.MarshalIndent(d, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// deleteCustomNodeDef refuses while any effective template still names the
// role — a template must never silently point at a missing node.
func deleteCustomNodeDef(id string) error {
	if _, builtin := nodeDefinitions[id]; builtin {
		return errors.New("built-in nodes cannot be deleted")
	}
	for _, t := range effectiveFlowTemplates() {
		for _, role := range flattenStages(templateStages(t)) {
			if role == id {
				return fmt.Errorf("node is used by template %q", t.ID)
			}
		}
		for _, e := range t.Edges {
			for _, role := range e.Append {
				if role == id {
					return fmt.Errorf("node is used by template %q", t.ID)
				}
			}
		}
	}
	for _, f := range loadFlows() {
		if f.Status != "queued" && f.Status != "running" {
			continue
		}
		for _, role := range append(append([]string{}, f.NodeRoles...), flattenStages(f.Stages)...) {
			if role == id {
				return fmt.Errorf("node is used by active flow %q", f.ID)
			}
		}
		for _, n := range f.Nodes {
			if n.Role == id {
				return fmt.Errorf("node is used by active flow %q", f.ID)
			}
		}
	}
	if err := os.Remove(nodeDefPath(id)); err != nil {
		return err
	}
	_ = os.Remove(nodeRuntimePath(id))
	// Also drop the node's prompt + its version history. Otherwise both orphan on
	// disk, and worse: a later same-id recreate would have savePromptBody read
	// this deleted role's stale prompt as `old` and archive it as the NEW node's
	// "previous version" — phantom history from a different, deleted role.
	// handleNodeCreate rolls the def + prompt back together on failure; delete
	// must clean them together too. Best-effort, like the runtime remove above.
	_ = os.Remove(filepath.Join(nodesDir(), id+".md"))
	_ = os.RemoveAll(promptHistoryDir("node-" + id))
	return nil
}

func handleNodeCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		nodeDefinition
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, promptBodyMax+8*1024)).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if _, exists := loadCustomNodeDefs()[in.ID]; exists {
		http.Error(w, "node already exists", http.StatusConflict)
		return
	}
	if strings.TrimSpace(in.Prompt) == "" {
		http.Error(w, "a new node needs a prompt", http.StatusBadRequest)
		return
	}
	if err := saveCustomNodeDef(in.nodeDefinition); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := savePromptBody("node-"+in.ID, in.Prompt); err != nil {
		_ = os.Remove(nodeDefPath(in.ID)) // don't leave a promptless role behind
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, nodeCatalogViews())
}

func handleNodePut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var d nodeDefinition
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&d); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	d.ID = id
	nsMu.Lock()
	defer nsMu.Unlock()
	if _, exists := loadCustomNodeDefs()[id]; !exists {
		http.Error(w, "unknown custom node (built-ins use prompt/runtime overrides)", http.StatusNotFound)
		return
	}
	if err := saveCustomNodeDef(d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, nodeCatalogViews())
}

func handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nsMu.Lock()
	defer nsMu.Unlock()
	if err := deleteCustomNodeDef(id); err != nil {
		code := http.StatusBadRequest
		if os.IsNotExist(err) {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
