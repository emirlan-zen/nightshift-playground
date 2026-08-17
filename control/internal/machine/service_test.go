package machine

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServiceCachesReads(t *testing.T) {
	reads := 0
	service := NewService(func() Vitals {
		reads++
		return Vitals{Generated: int64(reads), CPUCount: 8, OK: true}
	})
	now := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	first, second := service.Current(), service.Current()
	if first.Generated != 1 || second.Generated != 1 || reads != 1 {
		t.Fatalf("reads = %d", reads)
	}
	now = now.Add(5 * time.Second)
	if service.Current().Generated != 2 || reads != 2 {
		t.Fatalf("expired cache reads = %d", reads)
	}
}

func TestHandlerReturnsVitals(t *testing.T) {
	handler := NewHandler(NewService(func() Vitals {
		return Vitals{Generated: 111, CPUCount: 8, Load1: 1.5, OK: true}
	}))
	response := httptest.NewRecorder()
	handler.Get(response, httptest.NewRequest("GET", "/api/vitals", nil))
	var vitals Vitals
	if err := json.Unmarshal(response.Body.Bytes(), &vitals); err != nil {
		t.Fatal(err)
	}
	if vitals.CPUCount != 8 || vitals.Load1 != 1.5 || !vitals.OK {
		t.Fatalf("vitals = %+v", vitals)
	}
}

func TestReadAlwaysStampsPortableFields(t *testing.T) {
	vitals := Read()
	if vitals.Generated == 0 || vitals.CPUCount == 0 {
		t.Fatalf("vitals = %+v", vitals)
	}
	if vitals.DiskTotalBytes == 0 || vitals.DiskUsedBytes > vitals.DiskTotalBytes {
		t.Fatalf("disk = %+v", vitals)
	}
}
