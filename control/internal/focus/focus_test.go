package focus

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testHandler(t *testing.T) (*Store, *Handler) {
	t.Helper()
	store := NewStore(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := store.Save("products", "# bets\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("projects", "# repos\n"); err != nil {
		t.Fatal(err)
	}
	return store, NewHandler(store)
}

func requestPut(handler *Handler, id, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/focus/{id}", handler.Put)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/focus/"+id, strings.NewReader(body)))
	return response
}

func TestStoreReturnsBothDocumentsAndMissingPlaceholder(t *testing.T) {
	store, _ := testHandler(t)
	collection := store.All()
	if len(collection.Files) != 2 || collection.Files[0].ID != "products" || collection.Files[0].ModifiedAt == 0 {
		t.Fatalf("collection = %+v", collection)
	}
	if err := os.Remove(store.path("projects")); err != nil {
		t.Fatal(err)
	}
	collection = store.All()
	if collection.Files[1].Content != "" || collection.Files[1].ModifiedAt != 0 {
		t.Fatalf("missing document = %+v", collection.Files[1])
	}
}

func TestPutRoundTripsAtomically(t *testing.T) {
	store, handler := testHandler(t)
	response := requestPut(handler, "products", `{"content":"# updated bets\n"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body)
	}
	var document Document
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Content != "# updated bets\n" || document.ModifiedAt == 0 {
		t.Fatalf("document = %+v", document)
	}
	body, err := os.ReadFile(store.path("products"))
	if err != nil || string(body) != document.Content {
		t.Fatalf("disk = %q err = %v", body, err)
	}
}

func TestPutRejectsUnknownInvalidAndOversizedDocuments(t *testing.T) {
	store, handler := testHandler(t)
	if response := requestPut(handler, "products.md", `{"content":"x"}`); response.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", response.Code)
	}
	if response := requestPut(handler, "products", "not json"); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", response.Code)
	}
	large := `{"content":"` + strings.Repeat("a", maxBody+1024) + `"}`
	if response := requestPut(handler, "products", large); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large status = %d", response.Code)
	}
	document, _ := store.Get("products")
	if document.Content != "# bets\n" {
		t.Fatalf("document changed after rejection: %q", document.Content)
	}
}
