package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"doc-rag-mcp/internal/docgen"
	"doc-rag-mcp/internal/filedocs"
)

func TestOutDir(t *testing.T) {
	if got := outDir(""); got != "stdout" {
		t.Errorf("out vazio deveria ser stdout, veio %q", got)
	}
	if got := outDir("/tmp/docs"); got != "/tmp/docs" {
		t.Errorf("out deveria passar direto, veio %q", got)
	}
}

func TestFirstNonEmptyPrefersFlag(t *testing.T) {
	if got := firstNonEmpty("flag", "env", "cfg"); got != "flag" {
		t.Errorf("esperava flag, veio %q", got)
	}
	if got := firstNonEmpty("", "env", "cfg"); got != "env" {
		t.Errorf("esperava env, veio %q", got)
	}
	if got := firstNonEmpty("", "", "cfg"); got != "cfg" {
		t.Errorf("esperava cfg, veio %q", got)
	}
}

// fakeGenerator implementa docgen.Provider sem chamar nenhum LLM.
type fakeGenerator struct {
	calls int
}

func (f *fakeGenerator) Generate(ctx context.Context, prompt string) (string, error) {
	f.calls++
	return "## Propósito\nDoc de teste.", nil
}
func (f *fakeGenerator) Name() string { return "fake-gen" }

// TestRunMarkdownOnly gera .md sem Qdrant (Store=false): prova o caminho
// só-markdown do docgen standalone.
func TestRunMarkdownOnly(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("linha de conteudo relevante para documentar\n", 25)
	if err := os.WriteFile(filepath.Join(dir, "grande.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pequeno.go"), []byte("package p\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	g := &fakeGenerator{}
	st, err := filedocs.Run(context.Background(), "proj", dir, nil, nil, g, filedocs.Options{
		Store: false, OutDir: out, Model: "fake",
		MaxBytes: docgen.DefaultMaxBytes, MinLines: 20, Concurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 || st.Generated != 1 {
		t.Fatalf("esperava 1 candidato (>=20 linhas) gerado: %+v", st)
	}
	data, err := os.ReadFile(filepath.Join(out, "proj", "grande.go.md"))
	if err != nil {
		t.Fatalf("markdown esperado não foi escrito: %v", err)
	}
	if !strings.Contains(string(data), "Doc de teste") || !strings.Contains(string(data), "pode conter erros") {
		t.Fatalf("markdown deveria ter a doc + aviso de LLM: %s", data)
	}
	if g.calls != 1 {
		t.Fatalf("esperava 1 completion, houve %d", g.calls)
	}
}
