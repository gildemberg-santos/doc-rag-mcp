package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doc-rag-mcp/internal/vector"
)

type fakeEmbed struct {
	vec []float32
	err error
}

func (f *fakeEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = f.vec
	}
	return out, f.err
}
func (f *fakeEmbed) EmbedQuery(ctx context.Context, q string) ([]float32, error) { return f.vec, f.err }
func (f *fakeEmbed) Dims() int                                                   { return len(f.vec) }
func (f *fakeEmbed) Name() string                                                { return "fake" }

func newFakeQdrant(t *testing.T, fn func(method, path string, body map[string]any) (int, any)) *vector.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		status, resp := fn(r.Method, r.URL.Path, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return vector.New(srv.URL, "testcol")
}

func TestHealthReportsOK(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.Health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("esperava status=ok, veio %v", body)
	}
}

func TestSearchRequiresQueryParam(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest(http.MethodGet, "/search", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400 sem ?q=, veio %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "obrigatório") {
		t.Fatalf("mensagem de erro errada: %q", rec.Body.String())
	}
}

func TestSearchReturnsResultsEnvelope(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "1", "score": 0.9, "payload": map[string]any{
				"project": "proj", "path": "a.rb", "language": "rb", "content": "conteudo",
			}},
		}}}
	})
	h := &Handler{Qdrant: q, Embedder: &fakeEmbed{vec: []float32{0.1}}}
	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest(http.MethodGet, "/search?q=teste", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Query   string `json:"query"`
		Results []any  `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Query != "teste" {
		t.Errorf("query no envelope errada: %q", body.Query)
	}
	if len(body.Results) != 1 {
		t.Errorf("esperava 1 resultado, veio %d", len(body.Results))
	}
}

func TestSearchPropagatesSearchErrorAsBadGateway(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{}
	})
	h := &Handler{Qdrant: q, Embedder: &fakeEmbed{err: context.DeadlineExceeded}}
	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest(http.MethodGet, "/search?q=teste", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("esperava 502 quando a busca falha, veio %d", rec.Code)
	}
}

func TestSearchClampsInvalidKToDefault(t *testing.T) {
	var gotLimit float64
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		if l, ok := body["limit"].(float64); ok {
			gotLimit = l
		}
		return 200, map[string]any{"result": map[string]any{"points": []any{}}}
	})
	h := &Handler{Qdrant: q, Embedder: &fakeEmbed{vec: []float32{0.1}}}
	rec := httptest.NewRecorder()
	// k=999 é inválido (>50) -> TopK cai no default (5) -> poolLimit = 5*4 = 20
	h.Search(rec, httptest.NewRequest(http.MethodGet, "/search?q=teste&k=999", nil))

	if gotLimit != 20 {
		t.Fatalf("k inválido deveria cair no default (pool=20), veio limit=%v", gotLimit)
	}
}
