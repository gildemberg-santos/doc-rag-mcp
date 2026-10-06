package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type capturedRequest struct {
	method string
	path   string
	body   map[string]any
}

// newFakeQdrant monta um httptest.Server e um Client apontando pra ele.
// handler decide o que responder; toda requisição recebida é anexada em
// *reqs (na ordem), com o body já decodificado, pra inspeção nos testes.
func newFakeQdrant(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) (*Client, *[]capturedRequest) {
	t.Helper()
	reqs := &[]capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		*reqs = append(*reqs, capturedRequest{method: r.Method, path: r.URL.Path, body: body})
		handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "testcol"), reqs
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	c := New("http://localhost:6333/", "docs")
	if c.BaseURL != "http://localhost:6333" {
		t.Fatalf("esperava barra final removida, veio %q", c.BaseURL)
	}
}

func TestEnsureCollectionSkipsCreateWhenExists(t *testing.T) {
	putCalled := false
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{"result": map[string]any{}})
		case http.MethodPut:
			putCalled = true
			writeJSON(w, 200, map[string]any{})
		}
	})
	if err := c.EnsureCollection(context.Background(), 768); err != nil {
		t.Fatal(err)
	}
	if putCalled {
		t.Fatal("coleção já existe (GET 200) — não deveria ter chamado PUT para recriar")
	}
}

func TestEnsureCollectionCreatesWhenMissing(t *testing.T) {
	var createBody map[string]any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 404, map[string]any{"status": map[string]any{"error": "not found"}})
		case http.MethodPut:
			createBody = body
			writeJSON(w, 200, map[string]any{})
		}
	})
	if err := c.EnsureCollection(context.Background(), 768); err != nil {
		t.Fatal(err)
	}
	if createBody == nil {
		t.Fatal("esperava um PUT criando a coleção quando GET não é 200")
	}
	vectors, _ := createBody["vectors"].(map[string]any)
	if vectors == nil {
		t.Fatalf("payload de criação sem campo 'vectors': %v", createBody)
	}
	if size, _ := vectors["size"].(float64); int(size) != 768 {
		t.Errorf("esperava vectors.size=768, veio %v", vectors["size"])
	}
	if dist, _ := vectors["distance"].(string); dist != "Cosine" {
		t.Errorf("esperava vectors.distance=Cosine, veio %v", vectors["distance"])
	}
}

func TestUpsertNoOpOnEmptySlice(t *testing.T) {
	called := false
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		called = true
		writeJSON(w, 200, map[string]any{})
	})
	if err := c.Upsert(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("Upsert com slice vazia não deveria fazer nenhuma chamada HTTP")
	}
}

func TestUpsertBatchesInGroupsOf128(t *testing.T) {
	var batchSizes []int
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		pts, _ := body["points"].([]any)
		batchSizes = append(batchSizes, len(pts))
		writeJSON(w, 200, map[string]any{})
	})
	points := make([]Point, 130)
	for i := range points {
		points[i] = Point{ID: "id", Vector: []float32{0.1}}
	}
	if err := c.Upsert(context.Background(), points); err != nil {
		t.Fatal(err)
	}
	if len(batchSizes) != 2 || batchSizes[0] != 128 || batchSizes[1] != 2 {
		t.Fatalf("esperava lotes [128 2], veio %v", batchSizes)
	}
}

func TestUpsertPropagatesServerError(t *testing.T) {
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeJSON(w, 500, map[string]any{"status": "error"})
	})
	err := c.Upsert(context.Background(), []Point{{ID: "x", Vector: []float32{0.1}}})
	if err == nil {
		t.Fatal("esperava erro quando o servidor responde 500")
	}
}

func TestDeletePointsNoOpOnEmptySlice(t *testing.T) {
	called := false
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		called = true
		writeJSON(w, 200, map[string]any{})
	})
	if err := c.DeletePoints(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("DeletePoints com slice vazia não deveria fazer nenhuma chamada HTTP")
	}
}

func TestDeletePointsBatchesInGroupsOf128(t *testing.T) {
	var batchSizes []int
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		ids, _ := body["points"].([]any)
		batchSizes = append(batchSizes, len(ids))
		writeJSON(w, 200, map[string]any{})
	})
	ids := make([]string, 300)
	for i := range ids {
		ids[i] = "id"
	}
	if err := c.DeletePoints(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(batchSizes) != 3 || batchSizes[0] != 128 || batchSizes[1] != 128 || batchSizes[2] != 44 {
		t.Fatalf("esperava lotes [128 128 44], veio %v", batchSizes)
	}
}

func TestDeleteByProjectSendsProjectFilter(t *testing.T) {
	var got map[string]any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		got = body
		writeJSON(w, 200, map[string]any{})
	})
	if err := c.DeleteByProject(context.Background(), "meu-projeto"); err != nil {
		t.Fatal(err)
	}
	filter, _ := got["filter"].(map[string]any)
	must, _ := filter["must"].([]any)
	if len(must) != 1 {
		t.Fatalf("esperava filtro com 1 condição 'must', veio %v", got)
	}
	cond := must[0].(map[string]any)
	if cond["key"] != "project" {
		t.Errorf("esperava filtrar por key=project, veio %v", cond)
	}
	match := cond["match"].(map[string]any)
	if match["value"] != "meu-projeto" {
		t.Errorf("esperava match.value=meu-projeto, veio %v", match)
	}
}

func TestSearchDefaultsLimitAndOmitsFilterWithoutProject(t *testing.T) {
	var got map[string]any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		got = body
		writeJSON(w, 200, map[string]any{"result": map[string]any{"points": []any{}}})
	})
	if _, err := c.Search(context.Background(), []float32{0.1, 0.2}, "", 0); err != nil {
		t.Fatal(err)
	}
	if limit, _ := got["limit"].(float64); int(limit) != 5 {
		t.Errorf("limit<=0 deveria virar default 5, veio %v", got["limit"])
	}
	if _, has := got["filter"]; has {
		t.Errorf("sem projeto, não deveria mandar filtro: %v", got)
	}
}

func TestSearchAddsProjectFilterWhenGiven(t *testing.T) {
	var got map[string]any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		got = body
		writeJSON(w, 200, map[string]any{"result": map[string]any{"points": []any{}}})
	})
	if _, err := c.Search(context.Background(), []float32{0.1}, "proj-x", 10); err != nil {
		t.Fatal(err)
	}
	if _, has := got["filter"]; !has {
		t.Fatalf("com projeto informado, esperava filtro no payload: %v", got)
	}
}

func TestSearchParsesScoredPoints(t *testing.T) {
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeJSON(w, 200, map[string]any{
			"result": map[string]any{
				"points": []any{
					map[string]any{"id": "a", "score": 0.91, "payload": map[string]any{"path": "x.rb"}},
					map[string]any{"id": "b", "score": 0.80, "payload": map[string]any{"path": "y.rb"}},
				},
			},
		})
	})
	pts, err := c.Search(context.Background(), []float32{0.1}, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("esperava 2 pontos, veio %d", len(pts))
	}
	if pts[0].ID != "a" || pts[0].Score != 0.91 || pts[0].Payload["path"] != "x.rb" {
		t.Errorf("ponto 0 não bate: %+v", pts[0])
	}
}

func TestScrollSendsWithPayloadTrue(t *testing.T) {
	var got map[string]any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		got = body
		writeJSON(w, 200, map[string]any{"result": map[string]any{"points": []any{}}})
	})
	if _, _, err := c.Scroll(context.Background(), nil, 500, nil); err != nil {
		t.Fatal(err)
	}
	if wp, _ := got["with_payload"].(bool); !wp {
		t.Errorf("Scroll deveria pedir with_payload=true, veio %v", got["with_payload"])
	}
}

func TestScrollFieldsSendsFieldList(t *testing.T) {
	var got map[string]any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		got = body
		writeJSON(w, 200, map[string]any{"result": map[string]any{"points": []any{}}})
	})
	if _, _, err := c.ScrollFields(context.Background(), nil, 500, nil, []string{"hash", "path"}); err != nil {
		t.Fatal(err)
	}
	fields, ok := got["with_payload"].([]any)
	if !ok || len(fields) != 2 || fields[0] != "hash" || fields[1] != "path" {
		t.Fatalf("esperava with_payload=[hash,path], veio %v", got["with_payload"])
	}
}

func TestScrollRoundTripsOffsetAndNextPageOffset(t *testing.T) {
	var sentOffset any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		sentOffset = body["offset"]
		writeJSON(w, 200, map[string]any{
			"result": map[string]any{"points": []any{}, "next_page_offset": "abc-123"},
		})
	})
	_, next, err := c.Scroll(context.Background(), nil, 500, "offset-anterior")
	if err != nil {
		t.Fatal(err)
	}
	if sentOffset != "offset-anterior" {
		t.Errorf("esperava enviar offset recebido, veio %v", sentOffset)
	}
	if next != "abc-123" {
		t.Errorf("esperava repassar next_page_offset do servidor, veio %v", next)
	}
}

func TestScrollOmitsOffsetWhenNil(t *testing.T) {
	var got map[string]any
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		got = body
		writeJSON(w, 200, map[string]any{"result": map[string]any{"points": []any{}}})
	})
	if _, _, err := c.Scroll(context.Background(), nil, 500, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := got["offset"]; has {
		t.Errorf("offset nil não deveria aparecer no payload: %v", got)
	}
}

func TestCountParsesResult(t *testing.T) {
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeJSON(w, 200, map[string]any{"result": map[string]any{"count": 57886}})
	})
	n, err := c.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 57886 {
		t.Fatalf("esperava 57886, veio %d", n)
	}
}

func TestDoWrapsNon2xxStatusWithBody(t *testing.T) {
	c, _ := newFakeQdrant(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeJSON(w, 400, map[string]any{"status": map[string]any{"error": "bad filter"}})
	})
	_, err := c.Count(context.Background())
	if err == nil {
		t.Fatal("esperava erro em status 400")
	}
	msg := err.Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "bad filter") {
		t.Errorf("mensagem de erro deveria citar status e corpo da resposta: %v", err)
	}
}
