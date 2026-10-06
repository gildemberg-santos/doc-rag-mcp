package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doc-rag-mcp/internal/vector"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeEmbed implementa embed.Provider sem bater em nenhuma API real.
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
func (f *fakeEmbed) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	return f.vec, f.err
}
func (f *fakeEmbed) Dims() int    { return len(f.vec) }
func (f *fakeEmbed) Name() string { return "fake" }

// newFakeQdrant monta um httptest.Server simulando as respostas do Qdrant
// que cada handler precisa, roteado por método+path. fn decide status e
// corpo de resposta; requisições fora do mapa de rotas conhecidas caem no
// default (200, {}).
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

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("CallToolResult sem conteúdo")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("conteúdo não é TextContent: %T", res.Content[0])
	}
	return tc.Text
}

func scoredPointsResult(pts []map[string]any) map[string]any {
	return map[string]any{"result": map[string]any{"points": pts}}
}

// ---------- handleSearchDocs ----------

func TestHandleSearchDocsRequiresQuery(t *testing.T) {
	d := &Deps{Embedder: &fakeEmbed{vec: []float32{0.1}}}
	res, _, _ := handleSearchDocs(context.Background(), d, SearchDocsArgs{Query: "  "})
	if !res.IsError || !strings.Contains(resultText(t, res), "query é obrigatória") {
		t.Fatalf("esperava erro de query vazia, veio: %+v", res)
	}
}

func TestHandleSearchDocsNoHits(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, scoredPointsResult(nil)
	})
	d := &Deps{Qdrant: q, Embedder: &fakeEmbed{vec: []float32{0.1}}}
	res, _, _ := handleSearchDocs(context.Background(), d, SearchDocsArgs{Query: "algo"})
	if res.IsError {
		t.Fatalf("zero hits não é erro, é um resultado textual: %+v", res)
	}
	if !strings.Contains(resultText(t, res), "Nenhum trecho encontrado") {
		t.Fatalf("mensagem inesperada: %q", resultText(t, res))
	}
}

func TestHandleSearchDocsFormatsHits(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, scoredPointsResult([]map[string]any{
			{"id": "1", "score": 0.9, "payload": map[string]any{
				"project": "proj", "path": "a.rb", "language": "rb", "content": "conteudo do chunk a",
			}},
		})
	})
	d := &Deps{Qdrant: q, Embedder: &fakeEmbed{vec: []float32{0.1}}}
	res, _, err := handleSearchDocs(context.Background(), d, SearchDocsArgs{Query: "chunk"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Encontrados 1 trechos em 1 arquivos") {
		t.Fatalf("cabeçalho de contagem errado: %q", text)
	}
	if !strings.Contains(text, "[proj] a.rb") || !strings.Contains(text, "conteudo do chunk a") {
		t.Fatalf("trecho não aparece formatado corretamente: %q", text)
	}
}

func TestHandleSearchDocsPropagatesEmbedError(t *testing.T) {
	d := &Deps{Qdrant: newFakeQdrant(t, func(m, p string, b map[string]any) (int, any) { return 200, map[string]any{} }),
		Embedder: &fakeEmbed{err: context.DeadlineExceeded}}
	res, _, _ := handleSearchDocs(context.Background(), d, SearchDocsArgs{Query: "x"})
	if !res.IsError || !strings.Contains(resultText(t, res), "falha na busca") {
		t.Fatalf("esperava erro de busca propagado, veio: %+v", res)
	}
}

// ---------- handleGrepDocs ----------

func TestHandleGrepDocsRequiresPattern(t *testing.T) {
	d := &Deps{}
	res, _, _ := handleGrepDocs(context.Background(), d, GrepDocsArgs{Pattern: ""})
	if !res.IsError || !strings.Contains(resultText(t, res), "pattern é obrigatório") {
		t.Fatalf("esperava erro de pattern vazio, veio: %+v", res)
	}
}

func TestHandleGrepDocsNoMatches(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{}}}
	})
	d := &Deps{Qdrant: q}
	res, _, _ := handleGrepDocs(context.Background(), d, GrepDocsArgs{Pattern: "inexistente"})
	if !strings.Contains(resultText(t, res), `Nenhum arquivo indexado contém "inexistente"`) {
		t.Fatalf("mensagem de zero resultados errada: %q", resultText(t, res))
	}
}

func TestHandleGrepDocsListsMatchingFiles(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "1", "payload": map[string]any{
				"project": "proj", "path": "a.rb", "language": "rb", "content": "tem webhook aqui",
			}},
		}}}
	})
	d := &Deps{Qdrant: q}
	res, _, err := handleGrepDocs(context.Background(), d, GrepDocsArgs{Pattern: "webhook"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, `Arquivos distintos com "webhook": 1`) {
		t.Fatalf("contagem errada: %q", text)
	}
	if !strings.Contains(text, "[proj] a.rb [rb]") {
		t.Fatalf("formatação do arquivo errada: %q", text)
	}
}

// ---------- handleGetDocument ----------

func TestHandleGetDocumentRequiresProjectAndPath(t *testing.T) {
	d := &Deps{}
	res, _, _ := handleGetDocument(context.Background(), d, GetDocArgs{Project: "", Path: ""})
	if !res.IsError {
		t.Fatalf("esperava erro sem project/path: %+v", res)
	}
}

func TestHandleGetDocumentNotFound(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{}}}
	})
	d := &Deps{Qdrant: q}
	res, _, _ := handleGetDocument(context.Background(), d, GetDocArgs{Project: "proj", Path: "nao-existe.rb"})
	if !strings.Contains(resultText(t, res), "não encontrado") {
		t.Fatalf("mensagem de não encontrado errada: %q", resultText(t, res))
	}
}

func TestHandleGetDocumentAssemblesChunksInOrder(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "2", "payload": map[string]any{"chunk_index": 1.0, "content": "parte dois"}},
			map[string]any{"id": "1", "payload": map[string]any{"chunk_index": 0.0, "content": "parte um"}},
		}}}
	})
	d := &Deps{Qdrant: q}
	res, _, err := handleGetDocument(context.Background(), d, GetDocArgs{Project: "proj", Path: "a.rb"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if strings.Index(text, "parte um") > strings.Index(text, "parte dois") {
		t.Fatalf("chunks fora de ordem (deveria ordenar por chunk_index): %q", text)
	}
}

// ---------- handleListProjects ----------

func TestHandleListProjectsEmpty(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{}}}
	})
	d := &Deps{Qdrant: q}
	res, _, _ := handleListProjects(context.Background(), d)
	if !strings.Contains(resultText(t, res), "Nenhum projeto indexado") {
		t.Fatalf("mensagem de vazio errada: %q", resultText(t, res))
	}
}

func TestHandleListProjectsCountsAndSorts(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "1", "payload": map[string]any{"project": "zebra", "indexed_at": "2026-01-01T00:00:00Z"}},
			map[string]any{"id": "2", "payload": map[string]any{"project": "abacate", "indexed_at": "2026-01-02T00:00:00Z"}},
			map[string]any{"id": "3", "payload": map[string]any{"project": "abacate", "indexed_at": "2026-01-03T00:00:00Z"}},
		}}}
	})
	d := &Deps{Qdrant: q}
	res, _, err := handleListProjects(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if strings.Index(text, "abacate") > strings.Index(text, "zebra") {
		t.Fatalf("projetos deveriam vir em ordem alfabética: %q", text)
	}
	if !strings.Contains(text, "abacate (2 chunks, atualizado em 2026-01-03T00:00:00Z)") {
		t.Fatalf("contagem/data mais recente errada: %q", text)
	}
}

// ---------- handleListDocs ----------

func TestHandleListDocsRequiresProject(t *testing.T) {
	d := &Deps{}
	res, _, _ := handleListDocs(context.Background(), d, ListDocsArgs{Project: ""})
	if !res.IsError {
		t.Fatalf("esperava erro sem project: %+v", res)
	}
}

func TestHandleListDocsFiltersByPrefix(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "1", "payload": map[string]any{"path": "app/models/user.rb", "language": "rb"}},
			map[string]any{"id": "2", "payload": map[string]any{"path": "spec/models/user_spec.rb", "language": "rb"}},
		}}}
	})
	d := &Deps{Qdrant: q}
	res, _, err := handleListDocs(context.Background(), d, ListDocsArgs{Project: "proj", Prefix: "app/"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "app/models/user.rb") {
		t.Fatalf("deveria incluir arquivo sob app/: %q", text)
	}
	if strings.Contains(text, "spec/models") {
		t.Fatalf("não deveria incluir arquivo fora do prefixo: %q", text)
	}
}

// ---------- handleIndexStatus ----------

func TestHandleIndexStatusReportsCount(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 200, map[string]any{"result": map[string]any{"count": 57886}}
	})
	d := &Deps{Qdrant: q, Provider: "ollama", Dims: 768}
	res, _, err := handleIndexStatus(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Pontos: 57886") || !strings.Contains(text, "Provider: ollama (768 dims)") {
		t.Fatalf("status formatado errado: %q", text)
	}
}

func TestHandleIndexStatusErrorWhenQdrantDown(t *testing.T) {
	q := newFakeQdrant(t, func(method, path string, body map[string]any) (int, any) {
		return 500, map[string]any{"status": "error"}
	})
	d := &Deps{Qdrant: q}
	res, _, _ := handleIndexStatus(context.Background(), d)
	if !res.IsError || !strings.Contains(resultText(t, res), "qdrant inacessível") {
		t.Fatalf("esperava erro de qdrant inacessível, veio: %+v", res)
	}
}
