package embed

import (
	"context"
	"fmt"
	"strings"
)

// Provider abstrai a origem dos embeddings (100% local via Ollama).
// Server e indexer devem usar o mesmo modelo de embedding + a mesma
// coleção, senão a busca não encontra nada (dims diferentes).
type Provider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, q string) ([]float32, error)
	Dims() int
	Name() string
}

// Pinger é uma capacidade opcional: checagem de alcançabilidade barata
// (sem gerar embedding de verdade, sem custo). Nem todo Provider precisa
// implementar — quem consome faz type assertion.
type Pinger interface {
	Ping(ctx context.Context) error
}

const (
	// ProviderOllama é o único provider suportado (local, grátis, offline).
	// O suporte a OpenAI foi removido — ver docs de migração no README.
	ProviderOllama = "ollama"
)

// NormalizeProvider valida o provider de embedding (default ollama).
// "openai" é rejeitado com erro explícito (removido, não apenas
// desconhecido) para não falhar de forma obscura em configs antigas.
func NormalizeProvider(p string) (string, error) {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" || p == ProviderOllama {
		return ProviderOllama, nil
	}
	if p == "openai" {
		return "", fmt.Errorf("provider %q foi removido (só modelos locais via ollama) — ajuste EMBED_PROVIDER/--provider para ollama", p)
	}
	return "", fmt.Errorf("EMBED_PROVIDER inválido %q (use ollama)", p)
}
