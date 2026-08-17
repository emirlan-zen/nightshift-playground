package control

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Deleting a custom node must also remove its prompt (nodes/<id>.md) and version
// history — otherwise they orphan on disk and a later same-id recreate inherits
// phantom history archived from the deleted role.
func TestDeleteCustomNodeDefCleansPromptAndHistory(t *testing.T) {
	flowEnv(t)
	id := "my-audit"
	create := func(prompt string) {
		body := `{"id":"` + id + `","name":"My audit","description":"d","effort":"high","minutes":60,"output":"o","prompt":"` + prompt + `"}`
		rr := httptest.NewRecorder()
		handleNodeCreate(rr, httptest.NewRequest("POST", "/api/nodes", strings.NewReader(body)))
		if rr.Code != 200 {
			t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
		}
	}

	create("first prompt body")
	// A prompt edit archives the old body into history.
	if _, err := savePromptBody("node-"+id, "second prompt body"); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(nodesDir(), id+".md")
	histDir := promptHistoryDir("node-" + id)
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("prompt not written: %v", err)
	}
	if entries, _ := os.ReadDir(histDir); len(entries) == 0 {
		t.Fatal("precondition: expected an archived history entry after the edit")
	}

	if err := deleteCustomNodeDef(id); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(nodeDefPath(id)); !os.IsNotExist(err) {
		t.Fatalf("node def not removed: %v", err)
	}
	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Fatalf("prompt orphaned after delete: %v", err)
	}
	if _, err := os.Stat(histDir); !os.IsNotExist(err) {
		t.Fatalf("prompt history orphaned after delete: %v", err)
	}

	// Recreating the same id must not inherit phantom history from the old role.
	create("brand new different body")
	if entries, _ := os.ReadDir(histDir); len(entries) != 0 {
		t.Fatalf("recreated node inherited phantom history: %v", entries)
	}
}
