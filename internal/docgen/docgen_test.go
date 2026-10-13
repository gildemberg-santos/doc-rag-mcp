package docgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromptForContainsAllSections(t *testing.T) {
	p := PromptFor("proj", "a/b.go", "go", "package b")
	for _, want := range []string{
		"proj", "a/b.go", "go", "package b",
		"português", "Propósito", "API pública", "Dependências",
		"Observações", "não invente", "segredos",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt deveria conter %q", want)
		}
	}
}

func TestOllamaGenerateSendsPromptAndParsesResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("esperava POST /api/generate, veio %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"## Propósito\nDoc gerada."}`))
	}))
	t.Cleanup(srv.Close)

	g := NewOllama(srv.URL, "qwen2.5-coder:7b", 8192, 1024, 0.2)
	doc, err := g.Generate(context.Background(), "documente X")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "Doc gerada") {
		t.Fatalf("resposta não propagada: %q", doc)
	}
	if gotBody["model"] != "qwen2.5-coder:7b" || gotBody["prompt"] != "documente X" || gotBody["stream"] != false {
		t.Errorf("body inesperado: %v", gotBody)
	}
	if g.Name() == "" {
		t.Error("Name() não deveria ser vazio")
	}
}

func TestOllamaGenerateEmptyResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":"   "}`))
	}))
	t.Cleanup(srv.Close)

	g := NewOllama(srv.URL, "m", 0, 0, 0)
	if _, err := g.Generate(context.Background(), "x"); err == nil {
		t.Fatal("geração vazia deveria ser erro (spike: qwen3 com think ligado retorna response vazio)")
	}
}

func TestOllamaGenerateNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	t.Cleanup(srv.Close)

	g := NewOllama(srv.URL, "modelo-inexistente", 0, 0, 0)
	if _, err := g.Generate(context.Background(), "x"); err == nil {
		t.Fatal("status != 200 deveria ser erro")
	}
}
