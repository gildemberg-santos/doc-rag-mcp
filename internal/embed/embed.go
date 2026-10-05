package embed

import (
	"context"
	"fmt"
	"strings"
)

// Provider abstrai a origem dos embeddings (OpenAI API ou Ollama local).
// Ambos os binários (server e indexer) devem usar o mesmo provider +
// a mesma coleção, senão a busca não encontra nada (dims diferentes).
type Provider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, q string) ([]float32, error)
	Dims() int
	Name() string
}

const (
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
)

// NormalizeProvider valida "openai" | "ollama" (default openai).
func NormalizeProvider(p string) (string, error) {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return ProviderOpenAI, nil
	}
	if p == ProviderOpenAI || p == ProviderOllama {
		return p, nil
	}
	return "", fmt.Errorf("EMBED_PROVIDER inválido %q (use openai|ollama)", p)
}

// CollectionFor resolve a coleção Qdrant por provider.
//
// openai -> base como está ("docs"): preserva o índice existente (1536 dims).
// ollama -> base + "-ollama" ("docs-ollama"): dims diferentes (768),
// recriar a coleção é obrigatório, então o índice OpenAI fica intacto.
func CollectionFor(provider, base string) string {
	if base == "" {
		base = "docs"
	}
	if provider == ProviderOllama {
		// evita sufixo duplo se o usuário já configurou "docs-ollama"
		if strings.HasSuffix(base, "-ollama") {
			return base
		}
		return base + "-ollama"
	}
	return base
}
