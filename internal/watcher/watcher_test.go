package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsIndexable(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"app/models/user.rb", true},
		{"app/controllers/application_controller.rb", true},
		{"main.go", true},
		{"script.py", true},
		{"README.md", true},
		{"config.yml", true},
		{"package.json", true},
		{"Dockerfile", true},
		{"Makefile", true},
		{"Gemfile.lock", false}, // lockfile não indexável (não está na lista)
		{".env", false},         // oculto
		{".git/config", false},  // oculto
		{"image.png", false},    // binário
		{"dist/bundle.js", true}, // .js é indexável (filtro de diretório dist/ é feito pelo chunker)
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isIndexable(tt.path); got != tt.want {
				t.Errorf("isIndexable(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestWatcherAddAndClose(t *testing.T) {
	dir := t.TempDir()
	w, err := New(100*time.Millisecond, func(paths []string) {})
	if err != nil {
		t.Fatalf("New() erro: %v", err)
	}
	defer w.Close()

	if err := w.Add(dir); err != nil {
		t.Fatalf("Add(%q) erro: %v", dir, err)
	}
	// Adicionar duas vezes não deve erro
	if err := w.Add(dir); err != nil {
		t.Fatalf("Add duplicada (%q) erro: %v", dir, err)
	}
}

func TestWatcherDebounce(t *testing.T) {
	dir := t.TempDir()
	var received [][]string
	w, err := New(100*time.Millisecond, func(paths []string) {
		received = append(received, paths)
	})
	if err != nil {
		t.Fatalf("New() erro: %v", err)
	}
	defer w.Close()

	if err := w.Add(dir); err != nil {
		t.Fatalf("Add(%q) erro: %v", dir, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Cria múltiplos arquivos rapidamente para testar debounce
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, fmt.Sprintf("arquivo%d.txt", i))
		if err := os.WriteFile(p, []byte("conteudo"), 0644); err != nil {
			t.Fatalf("WriteFile(%q) erro: %v", p, err)
		}
	}

	// Aguarda o debounce disparar e processar
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(received) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(received) == 0 {
		t.Fatal("nenhum evento recebido após debounce")
	}
	// Todos os 5 arquivos devem estar no lote (debounce agrupou)
	last := received[len(received)-1]
	if len(last) != 5 {
		t.Errorf("esperava 5 arquivos no lote do debounce, recebeu %d: %v", len(last), last)
	}
}
