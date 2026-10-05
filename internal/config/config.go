package config

import (
	"os"
	"strconv"
)

// Config centraliza todas as variáveis de ambiente do projeto.
type Config struct {
	QdrantURL        string
	Collection       string // base; coleção efetiva = embed.CollectionFor(provider, base)
	EmbedProvider    string // openai | ollama (default openai)
	OpenAIAPIKey     string
	OpenAIEmbedModel string
	EmbedDims        int // dims OpenAI (default 1536)
	OllamaURL        string
	OllamaModel      string
	OllamaDims       int // default 768 (nomic-embed-text)
	ProjectsRoot     string
	HTTPPort         string
	MCPName          string
	MCPVersion       string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load lê env com defaults sensatos para Docker e local.
func Load() Config {
	dims := 1536
	if v := os.Getenv("OPENAI_EMBED_DIMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			dims = n
		}
	}
	// text-embedding-3-large usa 3072 dims por padrão.
	model := getenv("OPENAI_EMBED_MODEL", "text-embedding-3-small")
	if os.Getenv("OPENAI_EMBED_DIMS") == "" && model == "text-embedding-3-large" {
		dims = 3072
	}
	return Config{
		QdrantURL:        getenv("QDRANT_URL", "http://localhost:6333"),
		Collection:       getenv("QDRANT_COLLECTION", "docs"),
		EmbedProvider:    getenv("EMBED_PROVIDER", "openai"),
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		OpenAIEmbedModel: model,
		EmbedDims:        dims,
		OllamaURL:        getenv("OLLAMA_URL", "http://localhost:11434"),
		OllamaModel:      getenv("OLLAMA_MODEL", "nomic-embed-text"),
		OllamaDims:       ollamaDims(),
		ProjectsRoot:     getenv("PROJECTS_ROOT", "/projects"),
		HTTPPort:         getenv("HTTP_PORT", "8080"),
		MCPName:          getenv("MCP_NAME", "doc-rag-mcp"),
		MCPVersion:       getenv("MCP_VERSION", "1.0.0"),
	}
}

func ollamaDims() int {
	if v := os.Getenv("OLLAMA_EMBED_DIMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 768
}
