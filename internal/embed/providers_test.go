package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- Ollama ----------

func TestNewOllamaDefaults(t *testing.T) {
	e := NewOllama("", "", 0)
	if e.BaseURL != "http://localhost:11434" {
		t.Errorf("BaseURL default errado: %q", e.BaseURL)
	}
	if e.Model != "nomic-embed-text" {
		t.Errorf("Model default errado: %q", e.Model)
	}
	if e.Dims() != 768 {
		t.Errorf("dims default deveria ser 768, veio %d", e.Dims())
	}
}

func TestNewOllamaMxbaiModelDims(t *testing.T) {
	e := NewOllama("http://x:1/", "mxbai-embed-large", 0)
	if e.Dims() != 1024 {
		t.Errorf("mxbai-embed-large deveria ter 1024 dims, veio %d", e.Dims())
	}
	if e.BaseURL != "http://x:1" {
		t.Errorf("BaseURL deveria perder a barra final, veio %q", e.BaseURL)
	}
}

func TestOllamaEmbedHappyPath(t *testing.T) {
	var gotModel string
	var gotInput []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, gotInput = body.Model, body.Input
		embeddings := make([][]float32, len(body.Input))
		for i := range embeddings {
			embeddings[i] = []float32{float32(i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
	}))
	defer srv.Close()

	e := NewOllama(srv.URL, "nomic-embed-text", 768)
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("esperava 2 vetores, veio %d", len(vecs))
	}
	if gotModel != "nomic-embed-text" {
		t.Errorf("modelo enviado errado: %q", gotModel)
	}
	if len(gotInput) != 2 || gotInput[0] != "a" || gotInput[1] != "b" {
		t.Errorf("input enviado errado: %v", gotInput)
	}
}

func TestOllamaEmbedBatchesIn32(t *testing.T) {
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		batchSizes = append(batchSizes, len(body.Input))
		embeddings := make([][]float32, len(body.Input))
		for i := range embeddings {
			embeddings[i] = []float32{0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
	}))
	defer srv.Close()

	e := NewOllama(srv.URL, "nomic-embed-text", 768)
	texts := make([]string, 35)
	for i := range texts {
		texts[i] = "x"
	}
	if _, err := e.Embed(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	if len(batchSizes) != 2 || batchSizes[0] != 32 || batchSizes[1] != 3 {
		t.Fatalf("esperava lotes [32 3], veio %v", batchSizes)
	}
}

func TestOllamaEmbedErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": `model "x" not found`})
	}))
	defer srv.Close()

	e := NewOllama(srv.URL, "nomic-embed-text", 768)
	_, err := e.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("esperava erro em status 404")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "ollama pull") {
		t.Errorf("mensagem deveria citar o status e sugerir 'ollama pull': %v", err)
	}
}

func TestOllamaEmbedErrorOnCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// devolve 1 embedding pra 2 textos pedidos
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0}}})
	}))
	defer srv.Close()

	e := NewOllama(srv.URL, "nomic-embed-text", 768)
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("esperava erro quando a contagem de embeddings não bate com a de textos")
	}
}

func TestOllamaEmbedQueryReturnsFirstVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 2, 3}}})
	}))
	defer srv.Close()

	e := NewOllama(srv.URL, "nomic-embed-text", 3)
	vec, err := e.EmbedQuery(context.Background(), "pergunta")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 3 || vec[0] != 1 {
		t.Fatalf("vetor inesperado: %v", vec)
	}
}

func TestOllamaEmbedErrorWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // porta fechada: conexão deve falhar

	e := NewOllama(url, "nomic-embed-text", 768)
	_, err := e.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("esperava erro de conexão com o servidor fechado")
	}
	if !strings.Contains(err.Error(), "ollama indisponível") {
		t.Errorf("mensagem deveria explicar que o ollama está indisponível: %v", err)
	}
}

// ---------- OpenAI ----------

func TestNewWithDimsDefaults(t *testing.T) {
	e := NewWithDims("key", "", 0)
	if e.Model != "text-embedding-3-small" || e.Dims() != 1536 {
		t.Errorf("default errado: model=%q dims=%d", e.Model, e.Dims())
	}
}

func TestNewWithDimsLargeModelDefaultsTo3072(t *testing.T) {
	e := NewWithDims("key", "text-embedding-3-large", 0)
	if e.Dims() != 3072 {
		t.Errorf("text-embedding-3-large deveria ter 3072 dims, veio %d", e.Dims())
	}
}

func TestNewWithDimsExplicitOverride(t *testing.T) {
	e := NewWithDims("key", "text-embedding-3-small", 256)
	if e.Dims() != 256 {
		t.Errorf("dims explícito deveria prevalecer, veio %d", e.Dims())
	}
}

func TestOpenAIEmbedFailsFastWithoutAPIKey(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	e := New("", "text-embedding-3-small")
	e.BaseURL = srv.URL
	_, err := e.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("esperava erro sem API key")
	}
	if called {
		t.Fatal("sem API key não deveria nem tentar chamar a API")
	}
}

func TestOpenAIEmbedSendsAuthHeaderAndReordersByIndex(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		// servidor devolve fora de ordem de propósito, pra provar que o
		// reordenamento por Index (não pela ordem de chegada) funciona.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{9, 9}, "index": 1},
				{"embedding": []float32{1, 1}, "index": 0},
			},
		})
	}))
	defer srv.Close()

	e := New("minha-chave", "text-embedding-3-small")
	e.BaseURL = srv.URL
	vecs, err := e.Embed(context.Background(), []string{"primeiro", "segundo"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer minha-chave" {
		t.Errorf("header Authorization errado: %q", gotAuth)
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][0] != 9 {
		t.Fatalf("reordenamento por index falhou: %v", vecs)
	}
}

func TestOpenAIEmbedBatchesIn64(t *testing.T) {
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body embedRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		batchSizes = append(batchSizes, len(body.Input))
		data := make([]map[string]any, len(body.Input))
		for i := range data {
			data[i] = map[string]any{"embedding": []float32{0}, "index": i}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	e := New("key", "text-embedding-3-small")
	e.BaseURL = srv.URL
	texts := make([]string, 70)
	for i := range texts {
		texts[i] = "x"
	}
	if _, err := e.Embed(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	if len(batchSizes) != 2 || batchSizes[0] != 64 || batchSizes[1] != 6 {
		t.Fatalf("esperava lotes [64 6], veio %v", batchSizes)
	}
}

func TestOpenAIEmbedErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "invalid api key"}})
	}))
	defer srv.Close()

	e := New("key-invalida", "text-embedding-3-small")
	e.BaseURL = srv.URL
	_, err := e.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("esperava erro em status 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("mensagem deveria citar o status: %v", err)
	}
}

func TestOpenAIEmbedQueryReturnsFirstVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{4, 5}, "index": 0}},
		})
	}))
	defer srv.Close()

	e := New("key", "text-embedding-3-small")
	e.BaseURL = srv.URL
	vec, err := e.EmbedQuery(context.Background(), "pergunta")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 2 || vec[0] != 4 {
		t.Fatalf("vetor inesperado: %v", vec)
	}
}
