package filedocs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"doc-rag-mcp/internal/vector"
)

func TestCollectionFor(t *testing.T) {
	if got := CollectionFor("docs"); got != "docs-filedocs" {
		t.Errorf("esperava docs-filedocs, veio %q", got)
	}
	if got := CollectionFor("docs-ollama"); got != "docs-ollama-filedocs" {
		t.Errorf("esperava docs-ollama-filedocs, veio %q", got)
	}
	if got := CollectionFor("docs-filedocs"); got != "docs-filedocs" {
		t.Errorf("sufixo duplo deveria ser evitado, veio %q", got)
	}
	if got := CollectionFor(""); got != "docs-filedocs" {
		t.Errorf("base vazia deveria virar docs-filedocs, veio %q", got)
	}
}

func TestPointIDStableAndDistinct(t *testing.T) {
	a := PointID("proj", "a/b.go")
	b := PointID("proj", "a/b.go")
	c := PointID("proj", "a/c.go")
	if a != b {
		t.Error("mesmo projeto+path deveria gerar o mesmo ID (upsert idempotente)")
	}
	if a == c {
		t.Error("paths diferentes deveriam gerar IDs diferentes")
	}
	if a == "" {
		t.Error("ID não deveria ser vazio")
	}
}

func TestSourceHashChangesWithContent(t *testing.T) {
	if SourceHash("aaa") != SourceHash("aaa") {
		t.Error("mesmo conteúdo deveria gerar o mesmo hash")
	}
	if SourceHash("aaa") == SourceHash("aab") {
		t.Error("conteúdos diferentes deveriam gerar hashes diferentes")
	}
}

// ---- fakes ----

type fakeGenerator struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeGenerator) Generate(ctx context.Context, prompt string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return "## Propósito\nDoc gerada para teste.", nil
}
func (f *fakeGenerator) Name() string { return "fake-gen" }
func (f *fakeGenerator) numCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeEmbedder struct {
	dims int
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, f.dims)
		out[i][0] = 0.5
	}
	return out, nil
}
func (f *fakeEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	v := make([]float32, f.dims)
	v[0] = 0.5
	return v, nil
}
func (f *fakeEmbedder) Dims() int    { return f.dims }
func (f *fakeEmbedder) Name() string { return "fake-embed" }

// fakeQdrant simula o Qdrant com armazenamento em memória de pontos da
// coleção filedocs (id → payload), roteando por método+path.
type fakeQdrant struct {
	t       *testing.T
	mu      sync.Mutex
	points  map[string]map[string]any // id → payload
	vectors map[string][]float32
	srv     *httptest.Server
}

func newFakeQdrant(t *testing.T) *fakeQdrant {
	fq := &fakeQdrant{t: t, points: map[string]map[string]any{}, vectors: map[string][]float32{}}
	fq.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.Header().Set("Content-Type", "application/json")
		fq.mu.Lock()
		defer fq.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/collections/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
				"status": "green", "optimizer_status": "ok",
				"points_count": len(fq.points), "indexed_vectors_count": len(fq.points),
				"segments_count": 1,
				"config": map[string]any{"params": map[string]any{
					"vectors": map[string]any{"size": 4, "distance": "Cosine"},
				}},
			}})
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/index"):
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
		case strings.HasSuffix(r.URL.Path, "/points") && r.Method == http.MethodPut:
			pts, _ := body["points"].([]any)
			for _, p := range pts {
				m, _ := p.(map[string]any)
				id, _ := m["id"].(string)
				fq.points[id] = m["payload"].(map[string]any)
				if v, ok := m["vector"].([]any); ok {
					vec := make([]float32, len(v))
					for i, x := range v {
						vec[i] = float32(x.(float64))
					}
					fq.vectors[id] = vec
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
		case strings.HasSuffix(r.URL.Path, "/points/scroll"):
			filterProj := ""
			if f, ok := body["filter"].(map[string]any); ok {
				if must, ok := f["must"].([]any); ok {
					for _, c := range must {
						cm, _ := c.(map[string]any)
						if cm["key"] == "project" {
							if mt, ok := cm["match"].(map[string]any); ok {
								filterProj, _ = mt["value"].(string)
							}
						}
					}
				}
			}
			fields, wantFields := body["with_payload"].([]any), false
			if fl, ok := body["with_payload"].([]any); ok {
				fields, wantFields = fl, true
			}
			var pts []map[string]any
			for id, p := range fq.points {
				if filterProj != "" && p["project"] != filterProj {
					continue
				}
				payload := p
				if wantFields {
					payload = map[string]any{}
					for _, f := range fields {
						fs, _ := f.(string)
						if v, ok := p[fs]; ok {
							payload[fs] = v
						}
					}
				} else if wp, ok := body["with_payload"].(bool); ok && !wp {
					payload = map[string]any{}
				}
				pts = append(pts, map[string]any{"id": id, "payload": payload})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": pts}})
		case strings.HasSuffix(r.URL.Path, "/points/delete"):
			if ids, ok := body["points"].([]any); ok {
				for _, id := range ids {
					delete(fq.points, id.(string))
				}
			} else if body["filter"] != nil {
				// DeleteByProject (Clean): apaga por projeto
				proj := ""
				if f, ok := body["filter"].(map[string]any); ok {
					if must, ok := f["must"].([]any); ok && len(must) > 0 {
						cm, _ := must[0].(map[string]any)
						if mt, ok := cm["match"].(map[string]any); ok {
							proj, _ = mt["value"].(string)
						}
					}
				}
				for id, p := range fq.points {
					if p["project"] == proj {
						delete(fq.points, id)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
		}
	}))
	t.Cleanup(fq.srv.Close)
	return fq
}

func (fq *fakeQdrant) client(collection string) *vector.Client {
	return vector.New(fq.srv.URL, collection)
}

func writeProject(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func bigContent(lines int) string {
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		sb.WriteString("linha de conteudo relevante para documentar // funcao exemplo\n")
	}
	return sb.String()
}

func TestRunPersistsAndSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, map[string]string{"a.go": bigContent(25)})
	fq := newFakeQdrant(t)
	q := fq.client("test-filedocs")
	g := &fakeGenerator{}
	e := &fakeEmbedder{dims: 4}

	st, err := Run(context.Background(), "proj", dir, q, e, g, Options{Model: "fake-model", Store: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 || st.Generated != 1 || st.Stored != 1 {
		t.Fatalf("1º ciclo deveria gerar+persistir 1: %+v", st)
	}
	if g.numCalls() != 1 {
		t.Fatalf("esperava 1 completion, houve %d", g.numCalls())
	}
	// 2º ciclo sem mudanças: pula sem chamar o LLM.
	st2, err := Run(context.Background(), "proj", dir, q, e, g, Options{Model: "fake-model", Store: true})
	if err != nil {
		t.Fatal(err)
	}
	if st2.Skipped != 1 || st2.Generated != 0 {
		t.Fatalf("2º ciclo deveria pular por source_hash: %+v", st2)
	}
	if g.numCalls() != 1 {
		t.Fatalf("nenhuma completion extra deveria ocorrer, houve %d", g.numCalls())
	}
	// Edição real: gera de novo.
	writeProject(t, dir, map[string]string{"a.go": bigContent(25) + "// nova funcao importante\n"})
	st3, err := Run(context.Background(), "proj", dir, q, e, g, Options{Model: "fake-model", Store: true})
	if err != nil {
		t.Fatal(err)
	}
	if st3.Generated != 1 {
		t.Fatalf("arquivo alterado deveria regerar: %+v", st3)
	}
}

func TestRunRemovesDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, map[string]string{"a.go": bigContent(25), "b.go": bigContent(25)})
	fq := newFakeQdrant(t)
	q := fq.client("test-filedocs")
	g := &fakeGenerator{}
	e := &fakeEmbedder{dims: 4}
	if _, err := Run(context.Background(), "proj", dir, q, e, g, Options{Model: "m", Store: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "b.go")); err != nil {
		t.Fatal(err)
	}
	st, err := Run(context.Background(), "proj", dir, q, e, g, Options{Model: "m", Store: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Removed != 1 {
		t.Fatalf("arquivo apagado deveria remover 1 ponto: %+v", st)
	}
	fq.mu.Lock()
	n := len(fq.points)
	fq.mu.Unlock()
	if n != 1 {
		t.Fatalf("esperava 1 ponto restante, há %d", n)
	}
}

func TestRunDryRunDoesNotMutate(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, map[string]string{"a.go": bigContent(25)})
	fq := newFakeQdrant(t)
	q := fq.client("test-filedocs")
	g := &fakeGenerator{}
	e := &fakeEmbedder{dims: 4}
	st, err := Run(context.Background(), "proj", dir, q, e, g, Options{Model: "m", Store: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Generated != 1 {
		t.Fatalf("dry-run deveria contar 1 a gerar: %+v", st)
	}
	if g.numCalls() != 0 {
		t.Fatalf("dry-run não deveria chamar o LLM, chamou %d", g.numCalls())
	}
	fq.mu.Lock()
	n := len(fq.points)
	fq.mu.Unlock()
	if n != 0 {
		t.Fatalf("dry-run não deveria persistir nada, há %d pontos", n)
	}
}

func TestBuildPointPayload(t *testing.T) {
	p := BuildPoint("proj", "a/b.go", "go", "doc", "hash123", "model-x", "2026-01-01T00:00:00Z", []float32{0.1})
	if p.ID == "" || len(p.Vector) != 1 {
		t.Fatalf("ponto malformado: %+v", p)
	}
	for _, k := range []string{"project", "path", "language", "doc_content", "source_hash", "model", "generated_at"} {
		if _, ok := p.Payload[k]; !ok {
			t.Errorf("payload deveria ter %q: %v", k, p.Payload)
		}
	}
	if p.Payload["project"] != "proj" || p.Payload["path"] != "a/b.go" {
		t.Errorf("project/path errados: %v", p.Payload)
	}
	// ID determinístico por arquivo.
	if p2 := BuildPoint("proj", "a/b.go", "go", "outra doc", "outro", "m", "t", []float32{0.2}); p2.ID != p.ID {
		t.Error("mesmo projeto+path deveria manter o ID (upsert idempotente)")
	}
}

func TestListCandidatesSkipsTinyFiles(t *testing.T) {
	dir := t.TempDir()
	writeProject(t, dir, map[string]string{
		"pequeno.md": strings.Repeat("curta\n", 5),
		"grande.md":  strings.Repeat("linha de conteudo relevante\n", 25),
	})
	got, err := ListCandidates(dir, 200*1024, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Rel != "grande.md" {
		t.Fatalf("esperava só grande.md, veio %+v", got)
	}
}

func TestIsModelNotFound(t *testing.T) {
	if !isModelNotFound(errTest("status=404 model=x")) {
		t.Error("status=404 deveria ser modelo ausente")
	}
	if isModelNotFound(errTest("ollama indisponível")) {
		t.Error("erro de conexão deveria ter retry")
	}
	if isModelNotFound(nil) {
		t.Error("nil não é modelo ausente")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

// missingModelGenerator simula Ollama sem o modelo (404 sempre).
type missingModelGenerator struct {
	calls int
}

func (f *missingModelGenerator) Generate(ctx context.Context, prompt string) (string, error) {
	f.calls++
	return "", errTest("ollama /api/generate falhou: status=404 model=m não encontrado (rode: ollama pull m)")
}
func (f *missingModelGenerator) Name() string { return "fake-missing" }

func TestGenerateWithRetrySkipsRetryOnMissingModel(t *testing.T) {
	g := &missingModelGenerator{}
	_, err := generateWithRetry(context.Background(), g, "x")
	if err == nil {
		t.Fatal("esperava erro de modelo ausente")
	}
	if g.calls != 1 {
		t.Fatalf("modelo ausente não deveria ter retry (cada tentativa custa uma geração): %d calls", g.calls)
	}
}
