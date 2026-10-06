package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusReportsCountsAndProjects(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		if strings.HasSuffix(path, "/points/count") {
			return 200, map[string]any{"result": map[string]any{"count": 3}}
		}
		return 200, map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "1", "payload": map[string]any{"project": "a", "indexed_at": "2026-01-01T00:00:00Z"}},
			map[string]any{"id": "2", "payload": map[string]any{"project": "b", "indexed_at": "2026-01-02T00:00:00Z"}},
			map[string]any{"id": "3", "payload": map[string]any{"project": "b", "indexed_at": "2026-01-03T00:00:00Z"}},
		}}}
	})
	h := &Handler{Qdrant: q, Embedder: &fakeEmbed{vec: []float32{0.1, 0.2}}}
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d: %s", rec.Code, rec.Body.String())
	}
	var resp StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Points != 3 || resp.Dims != 2 {
		t.Fatalf("resposta inesperada: %+v", resp)
	}
	if len(resp.Projects) != 2 {
		t.Fatalf("esperava 2 projetos, veio %d: %+v", len(resp.Projects), resp.Projects)
	}
	byName := map[string]ProjectStat{}
	for _, p := range resp.Projects {
		byName[p.Project] = p
	}
	if byName["b"].Chunks != 2 || byName["b"].UpdatedAt != "2026-01-03T00:00:00Z" {
		t.Fatalf("contagem/data mais recente do projeto 'b' errada: %+v", byName["b"])
	}
}

func TestStatusErrorWhenQdrantDown(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 500, map[string]any{"status": "error"}
	})
	h := &Handler{Qdrant: q, Embedder: &fakeEmbed{vec: []float32{0.1}}}
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("esperava 502 com Qdrant fora, veio %d", rec.Code)
	}
	var resp StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("esperava OK=false e Error preenchido: %+v", resp)
	}
}

func TestStatusCachesWithinTTL(t *testing.T) {
	calls := 0
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		if strings.HasSuffix(path, "/points/count") {
			calls++
			return 200, map[string]any{"result": map[string]any{"count": 1}}
		}
		return 200, map[string]any{"result": map[string]any{"points": []any{}}}
	})
	h := &Handler{Qdrant: q, Embedder: &fakeEmbed{vec: []float32{0.1}}}

	h.Status(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status", nil))
	h.Status(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status", nil))

	if calls != 1 {
		t.Fatalf("esperava 1 chamada real ao Qdrant (2ª veio do cache), houve %d", calls)
	}
}

func TestDashboardServesHTML(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.Dashboard(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("esperava Content-Type text/html, veio %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/status") {
		t.Fatalf("página deveria referenciar /status (mesma origem): não encontrado")
	}
}
