package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doc-rag-mcp/internal/config"
	"doc-rag-mcp/internal/vector"
)

// fakeEmbedder com Ping controlável.
type fakeEmbedder struct{ pingErr error }

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	return out, nil
}
func (f *fakeEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	return []float32{0.1}, nil
}
func (f *fakeEmbedder) Dims() int    { return 1 }
func (f *fakeEmbedder) Name() string { return "fake" }
func (f *fakeEmbedder) Ping(ctx context.Context) error {
	return f.pingErr
}

func fakeQdrant(t *testing.T, count int64, fail bool) *vector.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": count}})
	}))
	t.Cleanup(srv.Close)
	return vector.New(srv.URL, "testcol")
}

func testConfig() config.Config {
	return config.Config{QdrantURL: "http://qdrant:6333", OllamaURL: "http://ollama:11434", OllamaModel: "nomic-embed-text"}
}

func TestCheckDependenciesHealthy(t *testing.T) {
	q := fakeQdrant(t, 10, false)
	if warns := checkDependencies(context.Background(), q, &fakeEmbedder{}, testConfig()); len(warns) != 0 {
		t.Fatalf("tudo saudável deveria dar zero avisos, veio %v", warns)
	}
}

func TestCheckDependenciesWarnsActionable(t *testing.T) {
	q := fakeQdrant(t, 0, true)
	warns := checkDependencies(context.Background(), q, &fakeEmbedder{pingErr: errBoom{}}, testConfig())
	if len(warns) != 2 {
		t.Fatalf("esperava 2 avisos (qdrant + embedding), veio %v", warns)
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"docker compose up -d qdrant", "ollama pull nomic-embed-text"} {
		if !strings.Contains(joined, want) {
			t.Errorf("aviso deveria conter o comando de correção %q: %q", want, joined)
		}
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
