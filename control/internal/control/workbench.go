package control

// Workbench authoring surfaces (ADR-0016): safe, operator-owned editing for
// reusable flow templates and node prompts. Execution stays on the existing
// job/worktree machinery; these handlers only manage versioned definitions.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const promptBodyMax = 128 * 1024

var (
	flowTemplateIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	promptVersionRe  = regexp.MustCompile(`^[0-9]{10,20}$`)
)

type flowNodeCatalog struct {
	nodeDefinition
	Custom          bool   `json:"custom,omitempty"` // operator-defined (ADR-0017), deletable
	PromptID        string `json:"promptId"`
	Prompt          string `json:"prompt"`
	DefaultPrompt   string `json:"defaultPrompt"`
	PromptSource    string `json:"promptSource"`
	PromptRevision  string `json:"promptRevision"`
	RuntimeSource   string `json:"runtimeSource"`
	RuntimeRevision string `json:"runtimeRevision"`
	DefaultEffort   string `json:"defaultEffort"`
	DefaultMinutes  int    `json:"defaultMinutes"`
}

type nodeRuntimeOverride struct {
	Effort   string `json:"effort"`
	Minutes  int    `json:"minutes"`
	Executor string `json:"executor,omitempty"` // ADR-0017 seam; allowlist {claude, codex} since ADR-0018
	Model    string `json:"model,omitempty"`    // optional exact model id; must match the effective executor's family
}

type promptVersionView struct {
	ID       string `json:"id"`
	At       int64  `json:"at"`
	Size     int64  `json:"size"`
	Revision string `json:"revision"`
}

type promptDocument struct {
	ID          string              `json:"id"`
	Label       string              `json:"label"`
	Description string              `json:"description,omitempty"`
	Path        string              `json:"path"`
	Body        string              `json:"body"`
	DefaultBody string              `json:"defaultBody,omitempty"`
	Editable    bool                `json:"editable"`
	Source      string              `json:"source"`
	Revision    string              `json:"revision"`
	UpdatedAt   int64               `json:"updatedAt"`
	Versions    []promptVersionView `json:"versions"`
}

func defaultNodeFile(id string) string {
	def, ok := nodeDefinitions[id]
	if !ok {
		return ""
	}
	return "# Node · " + def.Name + "\n\n" + defaultNodePrompts[id] + "\n"
}

func promptRevision(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:6])
}

// flowAutomationRevision hashes everything about an automation that changes what
// a run DOES: each member's prompt and resolved runtime, and the graph itself —
// stages, routes, emitter declarations, and per-member runtime pins. A revision
// that ignored those would report "same automation" across a rewired graph or a
// model swap, and the eval trends (ADR-0023) would silently average two
// different experiments together.
func flowAutomationRevision(f flow) string {
	type pinnedNode struct {
		Role     string `json:"role"`
		Prompt   string `json:"prompt"`
		Effort   string `json:"effort"`
		Minutes  int    `json:"minutes"`
		Executor string `json:"executor,omitempty"`
		Model    string `json:"model,omitempty"`
	}
	members := f.NodeRoles
	if len(members) == 0 {
		members = flattenStages(effectiveStages(f))
	}
	pinned := make([]pinnedNode, 0, len(members))
	for _, member := range members {
		// A member slot (attempt#2) pins the SAME prompt as its base role —
		// that identity is what makes a harness comparison a comparison — but
		// keeps its own runtime, so the pair hashes to distinct graphs.
		role := baseRole(member)
		def, _ := effectiveNodeDefinition(role)
		def = applyMemberRuntime(def, f.MemberRuntime[member])
		pinned = append(pinned, pinnedNode{
			Role: member, Prompt: promptRevision(nodePrompt(role)), Effort: def.Effort, Minutes: def.Minutes,
			Executor: effectiveExecutor(def), Model: def.Model,
		})
	}
	b, _ := json.Marshal(struct {
		Nodes    []pinnedNode  `json:"nodes"`
		Stages   [][]string    `json:"stages,omitempty"`
		Edges    []routeEdge   `json:"edges,omitempty"`
		Emitters []emitterSpec `json:"emitters,omitempty"`
	}{pinned, effectiveStages(f), f.Edges, f.Emitters})
	return promptRevision(string(b))
}

func nodeCatalogViews() []flowNodeCatalog {
	customs := loadCustomNodeDefs()
	all := make(map[string]nodeDefinition, len(nodeDefinitions)+len(customs))
	for id, d := range nodeDefinitions {
		all[id] = d
	}
	for id, d := range customs {
		all[id] = d
	}
	out := make([]flowNodeCatalog, 0, len(all))
	for id, base := range all {
		_, custom := customs[id]
		def, runtimeSource := effectiveNodeDefinition(id)
		body := nodePrompt(id)
		fallback := defaultNodeFile(id)
		source := "built-in"
		if custom || body != fallback {
			source = "custom"
		}
		out = append(out, flowNodeCatalog{
			nodeDefinition: def, Custom: custom,
			PromptID: "node-" + id, Prompt: body, DefaultPrompt: fallback,
			PromptSource: source, PromptRevision: promptRevision(body),
			RuntimeSource:   runtimeSource,
			RuntimeRevision: promptRevision(fmt.Sprintf("%s:%d:%s:%s", def.Effort, def.Minutes, effectiveExecutor(def), def.Model)),
			DefaultEffort:   base.Effort, DefaultMinutes: base.Minutes,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func nodeRuntimeDir() string           { return filepath.Join(nsDir(), "node-runtime") }
func nodeRuntimePath(id string) string { return filepath.Join(nodeRuntimeDir(), id+".json") }

func effectiveNodeDefinition(id string) (nodeDefinition, string) {
	base, ok := nodeDefinitionByID(id)
	if !ok {
		return nodeDefinition{}, "missing"
	}
	b, err := os.ReadFile(nodeRuntimePath(id))
	if err != nil {
		return base, "built-in"
	}
	var override nodeRuntimeOverride
	if json.Unmarshal(b, &override) != nil || !validEfforts[override.Effort] || override.Minutes < runMinutesFloor || override.Minutes > runMinutesCeil || (override.Executor != "" && !validExecutors[override.Executor]) || (override.Model != "" && !modelIDRe.MatchString(override.Model)) {
		logf("node runtime %s ignored: invalid effort, minutes, executor, or model", id)
		return base, "built-in"
	}
	base.Effort, base.Minutes = override.Effort, override.Minutes
	if override.Executor != "" {
		base.Executor = override.Executor
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	return base, "custom"
}

// validateMemberRuntime checks a template's per-member runtime overrides
// (ADR-0023). Keys must name real stage members — a key that matches nothing is
// a silent no-op at 3am, which is exactly the off-graph failure this validation
// exists to kill. Values are PARTIAL: an empty field inherits, so a member that
// only re-points its executor keeps the role's effort and minutes.
func validateMemberRuntime(m map[string]nodeRuntimeOverride, stages [][]string) error {
	if len(m) == 0 {
		return nil
	}
	members := map[string]bool{}
	for _, stage := range stages {
		for _, member := range stage {
			members[member] = true
		}
	}
	for key, o := range m {
		if !members[key] {
			return fmt.Errorf("memberRuntime key %q is not a stage member", key)
		}
		if o.Effort != "" && !validEfforts[o.Effort] {
			return fmt.Errorf("memberRuntime %q: effort must be low|medium|high|xhigh|max", key)
		}
		if o.Minutes != 0 && (o.Minutes < runMinutesFloor || o.Minutes > runMinutesCeil) {
			return fmt.Errorf("memberRuntime %q: minutes must be %d-%d", key, runMinutesFloor, runMinutesCeil)
		}
		if o.Executor != "" && !validExecutors[o.Executor] {
			return fmt.Errorf("memberRuntime %q: executor must be claude|codex", key)
		}
		if o.Model != "" && !modelIDRe.MatchString(o.Model) {
			return fmt.Errorf("memberRuntime %q: model must be a plain model id", key)
		}
	}
	return nil
}

func saveNodeRuntime(id string, override nodeRuntimeOverride) error {
	if _, ok := nodeDefinitionByID(id); !ok {
		return errors.New("unknown node")
	}
	if !validEfforts[override.Effort] || override.Minutes < runMinutesFloor || override.Minutes > runMinutesCeil {
		return fmt.Errorf("effort must be low|medium|high|xhigh|max and minutes %d-%d", runMinutesFloor, runMinutesCeil)
	}
	if override.Executor != "" && !validExecutors[override.Executor] {
		return errors.New("executor must be claude|codex")
	}
	if override.Model != "" && !modelIDRe.MatchString(override.Model) {
		return errors.New("model must be a plain model id ([A-Za-z0-9._-], max 64)")
	}
	if err := os.MkdirAll(nodeRuntimeDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(override, "", "  ")
	path := nodeRuntimePath(id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func handleNodeRuntimePut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var override nodeRuntimeOverride
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&override); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if err := saveNodeRuntime(id, override); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	def, source := effectiveNodeDefinition(id)
	writeJSON(w, map[string]any{"id": id, "effort": def.Effort, "minutes": def.Minutes, "source": source})
}

func handleNodeRuntimeDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nsMu.Lock()
	_, known := nodeDefinitionByID(id)
	nsMu.Unlock()
	if !known {
		http.Error(w, "unknown node", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if err := os.Remove(nodeRuntimePath(id)); err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func flowTemplatesDir() string       { return filepath.Join(nsDir(), "flow-templates") }
func flowTemplateHistoryDir() string { return filepath.Join(nsDir(), "flow-template-history") }
func flowTemplatePath(id string) string {
	return filepath.Join(flowTemplatesDir(), id+".json")
}

func templateRevision(t flowTemplate) string {
	copy := t
	copy.Revision, copy.UpdatedAt = "", 0
	b, _ := json.Marshal(copy)
	return promptRevision(string(b))
}

type graphIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
	Role    string `json:"role,omitempty"`
	Stage   *int   `json:"stage,omitempty"`
	Edge    *int   `json:"edge,omitempty"`
}
type graphValidationError struct {
	Message string       `json:"error"`
	Issues  []graphIssue `json:"issues"`
}

func (e graphValidationError) Error() string { return e.Message }

func graphError(code, message, path, role string, stage, edge *int) error {
	return graphValidationError{Message: message, Issues: []graphIssue{{Code: code, Message: message, Path: path, Role: role, Stage: stage, Edge: edge}}}
}

func validateFlowTemplate(t flowTemplate) error {
	if !flowTemplateIDRe.MatchString(t.ID) {
		return errors.New("invalid template id")
	}
	t.Name, t.Description = strings.TrimSpace(t.Name), strings.TrimSpace(t.Description)
	if t.Name == "" || len(t.Name) > 100 || len(t.Description) > 600 {
		return errors.New("template name or description is invalid")
	}
	if len(t.Stages) == 0 && len(t.Nodes) == 0 {
		return graphError("empty-graph", "template wants nodes or stages", "stages", "", nil, nil)
	}
	stages := templateStages(t)
	// Lifetime node budget (ADR-0017): the run mints at most flowNodeMax
	// sessions. Enforce it here so an oversized graph is rejected on save, on
	// the graph, rather than surfacing at mint time (the exact off-graph
	// failure ADR-0020 set out to kill). Mirrors validateNodeRoles/validateEdges.
	if len(flattenStages(stages)) > flowNodeMax {
		return graphError("too-many-nodes", fmt.Sprintf("a run executes at most %d node sessions", flowNodeMax), "stages", "", nil, nil)
	}
	for si, stage := range stages {
		s := si
		if len(stage) == 0 {
			return graphError("empty-stage", "stage cannot be empty", fmt.Sprintf("stages.%d", si), "", &s, nil)
		}
		if len(stage) > flowStageMax {
			return graphError("stage-too-wide", fmt.Sprintf("stage has more than %d nodes", flowStageMax), fmt.Sprintf("stages.%d", si), "", &s, nil)
		}
		for mi, member := range stage {
			if strings.Contains(member, "#") && !isMemberSlot(member) {
				return graphError("invalid-member-slot", "member slot must be role#2..role#4", fmt.Sprintf("stages.%d.%d", si, mi), member, &s, nil)
			}
			if _, ok := nodeDefinitionByID(baseRole(member)); !ok {
				return graphError("unknown-node", "unknown node "+member, fmt.Sprintf("stages.%d.%d", si, mi), member, &s, nil)
			}
		}
	}
	if err := validateMemberSlots(stages); err != nil {
		return graphError("invalid-member-slot", err.Error(), "stages", "", nil, nil)
	}
	if err := validateMemberRuntime(t.MemberRuntime, stages); err != nil {
		return graphError("invalid-member-runtime", err.Error(), "memberRuntime", "", nil, nil)
	}
	if err := validateEmitters(t.Emitters, stages); err != nil {
		return graphError("invalid-emitter", err.Error(), "emitters", "", nil, nil)
	}
	if len(t.Edges) > flowNodeMax {
		return graphError("too-many-edges", fmt.Sprintf("a graph declares at most %d routes", flowNodeMax), "edges", "", nil, nil)
	}
	pathRoles := map[string]bool{}
	for _, stage := range stages {
		for _, role := range stage {
			pathRoles[role] = true
		}
	}
	for _, stage := range stages {
		for _, member := range stage {
			pathRoles[baseRole(member)] = true // a route may name the role or the exact member
		}
	}
	for ei, edge := range t.Edges {
		e := ei
		if edge.Verdict != "needs-work" {
			return graphError("unsupported-verdict", "only needs-work routes are supported", fmt.Sprintf("edges.%d.verdict", ei), edge.Node, nil, &e)
		}
		if !pathRoles[edge.Node] {
			return graphError("edge-source-off-path", "route source is not on the happy path", fmt.Sprintf("edges.%d.node", ei), edge.Node, nil, &e)
		}
		if len(edge.Append) == 0 {
			return graphError("empty-route", "route needs at least one appended node", fmt.Sprintf("edges.%d.append", ei), edge.Node, nil, &e)
		}
		if len(edge.Append) > flowNodeMax {
			return graphError("route-too-long", fmt.Sprintf("a route appends at most %d nodes", flowNodeMax), fmt.Sprintf("edges.%d.append", ei), edge.Node, nil, &e)
		}
		for ai, role := range edge.Append {
			if _, ok := nodeDefinitionByID(baseRole(role)); !ok {
				return graphError("unknown-node", "unknown node "+role, fmt.Sprintf("edges.%d.append.%d", ei, ai), role, nil, &e)
			}
		}
	}
	if err := validateTemplateSchedule(t.Schedule); err != nil {
		return graphError("invalid-schedule", err.Error(), "schedule", "", nil, nil)
	}
	return nil
}

func builtinTemplate(id string) (flowTemplate, bool) {
	for _, t := range flowTemplates {
		if t.ID == id {
			return t, true
		}
	}
	return flowTemplate{}, false
}

func loadCustomFlowTemplate(path string) (flowTemplate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return flowTemplate{}, err
	}
	var t flowTemplate
	if err := json.Unmarshal(b, &t); err != nil {
		return flowTemplate{}, err
	}
	if err := validateFlowTemplate(t); err != nil {
		return flowTemplate{}, err
	}
	if st, err := os.Stat(path); err == nil {
		t.UpdatedAt = st.ModTime().Unix()
	}
	t.Customized = true
	if _, ok := builtinTemplate(t.ID); ok {
		t.Builtin = true
	}
	t.Revision = templateRevision(t)
	return t, nil
}

func effectiveFlowTemplates() []flowTemplate {
	byID := map[string]flowTemplate{}
	order := make([]string, 0, len(flowTemplates))
	for _, t := range flowTemplates {
		t.Revision = templateRevision(t)
		byID[t.ID] = t
		order = append(order, t.ID)
	}
	matches, _ := filepath.Glob(filepath.Join(flowTemplatesDir(), "*.json"))
	sort.Strings(matches)
	for _, path := range matches {
		t, err := loadCustomFlowTemplate(path)
		if err != nil {
			logf("flow template %s ignored: %v", path, err)
			continue
		}
		if _, exists := byID[t.ID]; !exists {
			order = append(order, t.ID)
		}
		byID[t.ID] = t
	}
	out := make([]flowTemplate, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func saveFlowTemplate(t flowTemplate) error {
	if err := validateFlowTemplate(t); err != nil {
		return err
	}
	if err := os.MkdirAll(flowTemplatesDir(), 0o755); err != nil {
		return err
	}
	t.Stages = templateStages(t)
	t.Nodes = flattenStages(t.Stages)
	path := flowTemplatePath(t.ID)
	if old, err := os.ReadFile(path); err == nil {
		hist := filepath.Join(flowTemplateHistoryDir(), t.ID)
		if err := os.MkdirAll(hist, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(hist, strconv.FormatInt(time.Now().UnixNano(), 10)+".json"), old, 0o644)
		}
	}
	t.Builtin, t.Customized, t.Revision, t.UpdatedAt = false, false, "", 0
	b, _ := json.MarshalIndent(t, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func handleFlowTemplates(w http.ResponseWriter, _ *http.Request) {
	nsMu.Lock()
	defer nsMu.Unlock()
	writeJSON(w, effectiveFlowTemplates())
}

func handleFlowTemplatePut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !flowTemplateIDRe.MatchString(id) {
		http.Error(w, "invalid template id", http.StatusBadRequest)
		return
	}
	var t flowTemplate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&t); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	t.ID = id
	nsMu.Lock()
	defer nsMu.Unlock()
	if err := saveFlowTemplate(t); err != nil {
		var graphErr graphValidationError
		if errors.As(err, &graphErr) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(graphErr)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, _ := templateByID(id)
	writeJSON(w, saved)
}

func handleFlowTemplateDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !flowTemplateIDRe.MatchString(id) {
		http.Error(w, "invalid template id", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if err := os.Remove(flowTemplatePath(id)); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "template has no custom definition", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func promptHistoryDir(id string) string {
	return filepath.Join(nsDir(), "prompt-history", id)
}

func promptDescriptor(id string) (promptFile, string, bool) {
	doc, idx := buildPrompts()
	path, ok := idx[id]
	if !ok {
		return promptFile{}, "", false
	}
	for _, group := range doc.Groups {
		for _, file := range group.Files {
			if file.ID == id {
				return file, path, true
			}
		}
	}
	return promptFile{}, "", false
}

func listPromptVersions(id string) []promptVersionView {
	matches, _ := filepath.Glob(filepath.Join(promptHistoryDir(id), "*.md"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	out := make([]promptVersionView, 0, len(matches))
	for _, path := range matches {
		version := strings.TrimSuffix(filepath.Base(path), ".md")
		nano, err := strconv.ParseInt(version, 10, 64)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, promptVersionView{ID: version, At: nano / int64(time.Second), Size: int64(len(b)), Revision: promptRevision(string(b))})
		if len(out) == 20 {
			break
		}
	}
	return out
}

func promptDocumentFor(id string) (promptDocument, error) {
	file, path, ok := promptDescriptor(id)
	if !ok {
		return promptDocument{}, errors.New("unknown prompt id")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return promptDocument{}, err
	}
	body := string(b)
	editable := strings.HasPrefix(id, "node-")
	defaultBody := ""
	source := "file"
	if editable {
		role := strings.TrimPrefix(id, "node-")
		defaultBody = defaultNodeFile(role)
		source = "built-in"
		if body != defaultBody {
			source = "custom"
		}
	}
	updated := int64(0)
	if st, err := os.Stat(path); err == nil {
		updated = st.ModTime().Unix()
	}
	return promptDocument{
		ID: id, Label: file.Label, Description: file.Desc, Path: path,
		Body: body, DefaultBody: defaultBody, Editable: editable, Source: source,
		Revision: promptRevision(body), UpdatedAt: updated, Versions: listPromptVersions(id),
	}, nil
}

func savePromptBody(id, body string) (promptDocument, error) {
	if !strings.HasPrefix(id, "node-") {
		return promptDocument{}, errors.New("this prompt is read-only")
	}
	role := strings.TrimPrefix(id, "node-")
	if _, ok := nodeDefinitionByID(role); !ok {
		return promptDocument{}, errors.New("unknown node prompt")
	}
	if strings.TrimSpace(body) == "" || len(body) > promptBodyMax {
		return promptDocument{}, fmt.Errorf("prompt must contain 1-%d bytes", promptBodyMax)
	}
	path := filepath.Join(nodesDir(), role+".md")
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return promptDocument{}, err
	}
	if string(old) == body {
		return promptDocumentFor(id)
	}
	if len(old) > 0 {
		hist := promptHistoryDir(id)
		if err := os.MkdirAll(hist, 0o755); err != nil {
			return promptDocument{}, err
		}
		version := strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.WriteFile(filepath.Join(hist, version+".md"), old, 0o644); err != nil {
			return promptDocument{}, err
		}
	}
	if err := os.MkdirAll(nodesDir(), 0o755); err != nil {
		return promptDocument{}, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return promptDocument{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return promptDocument{}, err
	}
	return promptDocumentFor(id)
}

func handlePromptDocument(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	nsMu.Lock()
	defer nsMu.Unlock()
	doc, err := promptDocumentFor(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, doc)
}

func handlePromptDocumentPut(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, promptBodyMax+1024)).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	doc, err := savePromptBody(id, in.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, doc)
}

func handlePromptRestore(w http.ResponseWriter, r *http.Request) {
	id, version := r.URL.Query().Get("id"), r.URL.Query().Get("version")
	role := strings.TrimPrefix(id, "node-")
	if !strings.HasPrefix(id, "node-") || !promptVersionRe.MatchString(version) {
		http.Error(w, "invalid prompt or version", http.StatusBadRequest)
		return
	}
	if _, ok := nodeDefinitionByID(role); !ok {
		http.Error(w, "invalid prompt or version", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	b, err := os.ReadFile(filepath.Join(promptHistoryDir(id), version+".md"))
	if err != nil {
		http.Error(w, "version not found", http.StatusNotFound)
		return
	}
	doc, err := savePromptBody(id, string(b))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, doc)
}
