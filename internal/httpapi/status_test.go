package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"doc-rag-mcp/internal/dockerstat"
	"doc-rag-mcp/internal/history"
	"doc-rag-mcp/internal/indexerstatus"
	"doc-rag-mcp/internal/vector"
)

// mustServeDocker sobe um servidor HTTP de verdade sobre um socket Unix —
// o mesmo transporte que dockerstat.Client usa contra o docker.sock real.
func mustServeDocker(t *testing.T, sockPath string, containers []map[string]any) {
	t.Helper()
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{
		Listener: l,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(containers)
		})},
	}
	srv.Start()
	t.Cleanup(srv.Close)
}

// fakePingEmbed adiciona Ping() a fakeEmbed, pra testar a seção
// embed_health do /status (fakeEmbed sozinho não implementa embed.Pinger).
type fakePingEmbed struct {
	fakeEmbed
	pingErr error
}

func (f *fakePingEmbed) Ping(ctx context.Context) error { return f.pingErr }

// newRichFakeQdrant roteia por path exato — necessário pros testes que
// exercitam /collections/{name} (info) e /collections (lista), que o
// newFakeQdrant genérico (usado nos testes mais simples) não distingue.
func newRichFakeQdrant(t *testing.T, collectionInfo, collectionsList map[string]any, count int64, scrollPoints []map[string]any) *vector.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections/testcol":
			json.NewEncoder(w).Encode(collectionInfo)
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			json.NewEncoder(w).Encode(collectionsList)
		case strings.HasSuffix(r.URL.Path, "/points/count"):
			// CountFor de coleção órfã usa o mesmo sufixo — responde o
			// count genérico; teste de órfãs só confere o nome listado.
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": count}})
		case strings.HasSuffix(r.URL.Path, "/points/scroll"):
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": scrollPoints}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)
	return vector.New(srv.URL, "testcol")
}

func richInfo(status, optimizer string, points, indexed int64, segments, size int) map[string]any {
	return map[string]any{"result": map[string]any{
		"status": status, "optimizer_status": optimizer,
		"points_count": points, "indexed_vectors_count": indexed,
		"segments_count": segments,
		"config": map[string]any{"params": map[string]any{
			"vectors": map[string]any{"size": size, "distance": "Cosine"},
		}},
	}}
}

func richList(names ...string) map[string]any {
	cols := []map[string]any{}
	for _, n := range names {
		cols = append(cols, map[string]any{"name": n})
	}
	return map[string]any{"result": map[string]any{"collections": cols}}
}

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
	h := New(q, &fakeEmbed{vec: []float32{0.1, 0.2}})
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
	h := New(q, &fakeEmbed{vec: []float32{0.1}})
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
	h := New(q, &fakeEmbed{vec: []float32{0.1}})

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
	for _, want := range []string{"/status", "/status/history", "indexer", "containers", "Coleções órfãs"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard deveria mencionar %q (N1+N2+N3): não encontrado", want)
		}
	}
}

// Nível 1: saúde detalhada da coleção aparece no /status.
func TestStatusIncludesCollectionHealth(t *testing.T) {
	q := newRichFakeQdrant(t,
		richInfo("green", "ok", 57990, 60754, 8, 768),
		richList("testcol"),
		3, []map[string]any{},
	)
	h := New(q, &fakeEmbed{vec: make([]float32, 768)})
	h.Embedder = &fakeEmbed{vec: make([]float32, 768)}

	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.CollectionHealth == nil {
		t.Fatal("esperava collection_health preenchido")
	}
	got := resp.CollectionHealth
	if got.Status != "green" || got.OptimizerStatus != "ok" || got.SegmentsCount != 8 {
		t.Fatalf("health errado: %+v", got)
	}
	if got.PointsCount != 57990 || got.IndexedVectorsCount != 60754 || got.VectorSize != 768 {
		t.Fatalf("contagens/dims errados: %+v", got)
	}
	if resp.DimsMismatch {
		t.Fatal("dims batem (768==768) — dims_mismatch deveria ser false")
	}
}

// Nível 1: dims diferentes do provider viram aviso ativo.
func TestStatusFlagsDimsMismatch(t *testing.T) {
	q := newRichFakeQdrant(t,
		richInfo("green", "ok", 10, 10, 2, 1024), // coleção com 1024
		richList("testcol"),
		10, []map[string]any{},
	)
	h := New(q, &fakeEmbed{vec: make([]float32, 768)}) // provider com 768
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.DimsMismatch {
		t.Fatalf("esperava dims_mismatch=true (768 vs 1024): %+v", resp)
	}
}

// Nível 1: coleção de outra config aparece como órfã.
func TestStatusListsOrphanCollections(t *testing.T) {
	q := newRichFakeQdrant(t,
		richInfo("green", "ok", 5, 5, 1, 2),
		richList("testcol", "docs"),
		5, []map[string]any{},
	)
	h := New(q, &fakeEmbed{vec: []float32{0.1, 0.2}})
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.OtherCollections) != 1 || resp.OtherCollections[0].Name != "docs" {
		t.Fatalf("esperava 1 órfã 'docs', veio %+v", resp.OtherCollections)
	}
}

// Nível 1: ping do provider aparece como embed_health.
func TestStatusEmbedHealthOKAndFail(t *testing.T) {
	mk := func(pingErr error) StatusResponse {
		q := newRichFakeQdrant(t, richInfo("green", "ok", 1, 1, 1, 1), richList("testcol"), 1, []map[string]any{})
		h := New(q, &fakePingEmbed{fakeEmbed: fakeEmbed{vec: []float32{0.1}}, pingErr: pingErr})
		rec := httptest.NewRecorder()
		h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
		var resp StatusResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}
	if got := mk(nil); got.EmbedHealth == nil || !got.EmbedHealth.Checked || !got.EmbedHealth.OK {
		t.Fatalf("ping ok deveria dar checked+ok: %+v", got.EmbedHealth)
	}
	if got := mk(errors.New("boom")); got.EmbedHealth == nil || !got.EmbedHealth.Checked || got.EmbedHealth.OK || got.EmbedHealth.Error == "" {
		t.Fatalf("ping falho deveria dar checked, ok=false + error: %+v", got.EmbedHealth)
	}
}

// Nível 1: provider sem Ping() => checked=false, sem quebrar o /status.
func TestStatusEmbedHealthUncheckedWithoutPinger(t *testing.T) {
	q := newRichFakeQdrant(t, richInfo("green", "ok", 1, 1, 1, 1), richList("testcol"), 1, []map[string]any{})
	h := New(q, &fakeEmbed{vec: []float32{0.1}}) // sem Ping()
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.EmbedHealth == nil || resp.EmbedHealth.Checked {
		t.Fatalf("sem Pinger, checked deveria ser false: %+v", resp.EmbedHealth)
	}
}

// Nível 1: contadores de uso — Metrics conta /mcp e self expõe.
func TestStatusSelfReportsUsageCounters(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		if strings.HasSuffix(path, "/points/count") {
			return 200, map[string]any{"result": map[string]any{"count": 0}}
		}
		return 200, map[string]any{"result": map[string]any{"points": []any{}}}
	})
	h := New(q, &fakeEmbed{vec: []float32{0.1}})
	// simula 2 requisições MCP passando pelo middleware
	inner := Metrics(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for i := 0; i < 2; i++ {
		inner.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
	}
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Self.RequestsTotal != 2 {
		t.Fatalf("esperava requests_total=2, veio %+v", resp.Self)
	}
	if resp.Self.UptimeSeconds < 0 || resp.Self.Goroutines <= 0 {
		t.Fatalf("self stats implausíveis: %+v", resp.Self)
	}
}

// Nível 2: heartbeat do indexer aparece quando o arquivo existe,
// e some (sem quebrar) quando não existe.
func TestStatusIndexerSection(t *testing.T) {
	mkQ := func() *vector.Client {
		return newRichFakeQdrant(t, richInfo("green", "ok", 1, 1, 1, 1), richList("testcol"), 1, []map[string]any{})
	}
	// com arquivo válido
	path := filepath.Join(t.TempDir(), "status.json")
	if err := indexerstatus.Write(path, indexerstatus.Status{
		UpdatedAt: "2026-10-06T01:00:00Z", IntervalSeconds: 600,
		TotalChunks: 100, OKCount: 2, FailCount: 0,
	}); err != nil {
		t.Fatal(err)
	}
	h := New(mkQ(), &fakeEmbed{vec: []float32{0.1}})
	h.IndexerStatusFile = path
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Indexer == nil || resp.Indexer.TotalChunks != 100 {
		t.Fatalf("esperava seção indexer com 100 chunks: %+v", resp.Indexer)
	}
	// sem arquivo => seção omitida, status continua OK
	h2 := New(mkQ(), &fakeEmbed{vec: []float32{0.1}})
	h2.IndexerStatusFile = filepath.Join(t.TempDir(), "nao-existe.json")
	rec2 := httptest.NewRecorder()
	h2.Status(rec2, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp2 StatusResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if !resp2.OK || resp2.Indexer != nil {
		t.Fatalf("sem heartbeat, indexer deveria ser omitido com OK=true: %+v", resp2)
	}
}

// Nível 3: containers via socket fake (mesmo transporte unix do real).
func TestStatusContainersSection(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	// sobe HTTP sobre socket unix com 1 container parado + 1 rodando
	mustServeDocker(t, sock, []map[string]any{
		{"Id": "a", "Names": []string{"/doc-rag-mcp"}, "Image": "img", "State": "running", "Status": "Up 1h", "Created": 1},
		{"Id": "b", "Names": []string{"/doc-rag-qdrant"}, "Image": "img2", "State": "exited", "Status": "Exited", "Created": 1},
	})
	q := newRichFakeQdrant(t, richInfo("green", "ok", 1, 1, 1, 1), richList("testcol"), 1, []map[string]any{})
	h := New(q, &fakeEmbed{vec: []float32{0.1}})
	h.Docker = dockerstat.New(sock)
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Containers) != 2 {
		t.Fatalf("esperava 2 containers, veio %+v", resp.Containers)
	}
	// ordenado por nome
	if resp.Containers[0].Name != "doc-rag-mcp" || resp.Containers[1].State != "exited" {
		t.Fatalf("containers errados/fora de ordem: %+v", resp.Containers)
	}
}

// Nível 3: sem Docker configurado => seção omitida, sem erro.
func TestStatusOmitsContainersWithoutDocker(t *testing.T) {
	q := newRichFakeQdrant(t, richInfo("green", "ok", 1, 1, 1, 1), richList("testcol"), 1, []map[string]any{})
	h := New(q, &fakeEmbed{vec: []float32{0.1}})
	// Docker nil de propósito
	rec := httptest.NewRecorder()
	h.Status(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var resp StatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.OK || len(resp.Containers) != 0 {
		t.Fatalf("sem Docker, containers deveria ser omitido: %+v", resp)
	}
}

// Nível 3: /status/history serve a série temporal.
func TestHistoryEndpointServesSamples(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		if strings.HasSuffix(path, "/points/count") {
			return 200, map[string]any{"result": map[string]any{"count": 7}}
		}
		return 200, map[string]any{"result": map[string]any{"points": []any{}}}
	})
	h := New(q, &fakeEmbed{vec: []float32{0.1}})
	h.History = history.New("") // memória
	h.Status(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status", nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/status/history?n=10", nil)
	h.HistoryHandler(rec, req)
	var body struct {
		Samples []history.Sample `json:"samples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Samples) != 1 || body.Samples[0].Points != 7 {
		t.Fatalf("esperava 1 sample com points=7: %+v", body.Samples)
	}
}

// Nível 3: histórico persiste em arquivo quando configurado.
func TestHistoryPersistsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	h := New(nil, nil)
	h.History = history.New(path)
	h.History.Append(history.Sample{Points: 42})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "42") {
		t.Fatalf("arquivo deveria conter o sample: %s", data)
	}
	if got := h.History.Last(10); len(got) != 1 || got[0].Points != 42 {
		t.Fatalf("Last() deveria devolver o sample do arquivo: %+v", got)
	}
}

func TestHistoryEndpointEmptyWithoutSamples(t *testing.T) {
	h := New(nil, nil)
	h.History = history.New("")
	rec := httptest.NewRecorder()
	h.HistoryHandler(rec, httptest.NewRequest(http.MethodGet, "/status/history", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	samples, _ := body["samples"].([]any)
	if samples == nil || len(samples) != 0 {
		t.Fatalf("sem samples, deveria devolver lista vazia (não null): %v", body)
	}
}

// embedHealth com cache: 2º /status não re-pinga dentro do TTL.
func TestEmbedHealthCached(t *testing.T) {
	pings := 0
	q := newRichFakeQdrant(t, richInfo("green", "ok", 1, 1, 1, 1), richList("testcol"), 1, []map[string]any{})
	pinger := &countingPinger{err: nil, calls: &pings}
	h := New(q, pinger)
	h.Status(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status", nil))
	// força expiração só do cache de /status, não do embed (30s)
	h.statusMu.Lock()
	h.statusAt = time.Now().Add(-10 * time.Second)
	h.statusMu.Unlock()
	h.Status(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status", nil))
	if pings != 1 {
		t.Fatalf("esperava 1 ping (2º do cache), houve %d", pings)
	}
}

type countingPinger struct {
	fakeEmbed
	err   error
	calls *int
}

func (c *countingPinger) Ping(ctx context.Context) error {
	*c.calls++
	return c.err
}
func (c *countingPinger) Dims() int    { return 1 }
func (c *countingPinger) Name() string { return "fake" }
