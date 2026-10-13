package config

import (
	"os"
	"strconv"
)

// buildVersion carrega o git-sha via -ldflags (o Makefile injeta;
// vazio fora do make). Quando setado, MCP_VERSION vira "1.0.0+a1b2c3d" —
// permite ao agente detectar binário desatualizado via index_status.
var buildVersion string

// Config centraliza todas as variáveis de ambiente do projeto.
// Embeddings e geração são 100% locais via Ollama (sem API externa, sem key).
type Config struct {
	QdrantURL     string
	Collection    string // coleção Qdrant efetiva (filedocs usa <base>-filedocs)
	EmbedProvider string // reservado: só "ollama" é aceito (default ollama)
	OllamaURL     string
	OllamaModel   string // modelo de embedding (default nomic-embed-text)
	OllamaDims    int    // default 768 (nomic-embed-text)
	// Geração de documentação (internal/docgen) — modelo independente do
	// de embedding (DocGenModelOvr/DOCGEN_MODEL tem precedência).
	DocGenModelOvr string // override de DOCGEN_MODEL (vazio = default local)
	ProjectsRoot   string
	HTTPPort       string
	MCPName        string
	MCPVersion     string

	// Nível 2 — heartbeat do indexer --watch (arquivo compartilhado).
	// Vazio = seção omitida no /status.
	IndexerStatusFile string
	// Nível 3 — socket Docker (opt-in, privilegiado) + projeto compose
	// pra filtrar containers. Vazio = seção omitida.
	DockerSocket   string
	ComposeProject string
	// Nível 3 — série temporal: arquivo JSONL onde cada /status real
	// anexa um sample. Vazio = só buffer em memória.
	HistoryFile string
	// MCP_AUTH_TOKEN protege /mcp, /search, /status e /dashboard com
	// `Authorization: Bearer <token>`. Vazio = sem auth (rede confiável).
	AuthToken string
	// QDRANT_API_KEY autentica no Qdrant (`api-key`) quando o servidor
	// foi ligado com QDRANT_API_KEY. Vazio = Qdrant aberto.
	QdrantAPIKey string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load lê env com defaults sensatos para Docker e local.
func Load() Config {
	mcpVersion := getenv("MCP_VERSION", "1.0.0")
	if buildVersion != "" {
		mcpVersion += "+" + buildVersion
	}
	return Config{
		QdrantURL:         getenv("QDRANT_URL", "http://localhost:6333"),
		Collection:        getenv("QDRANT_COLLECTION", "docs"),
		EmbedProvider:     getenv("EMBED_PROVIDER", "ollama"),
		OllamaURL:         getenv("OLLAMA_URL", "http://localhost:11434"),
		OllamaModel:       getenv("OLLAMA_MODEL", "nomic-embed-text"),
		OllamaDims:        ollamaDims(),
		DocGenModelOvr:    os.Getenv("DOCGEN_MODEL"),
		ProjectsRoot:      getenv("PROJECTS_ROOT", "/projects"),
		HTTPPort:          getenv("HTTP_PORT", "8080"),
		MCPName:           getenv("MCP_NAME", "doc-rag-mcp"),
		MCPVersion:        mcpVersion,
		IndexerStatusFile: os.Getenv("INDEXER_STATUS_FILE"),
		DockerSocket:      os.Getenv("DOCKER_SOCKET"),
		ComposeProject:    getenv("COMPOSE_PROJECT_NAME", ""),
		HistoryFile:       os.Getenv("STATUS_HISTORY_FILE"),
		AuthToken:         os.Getenv("MCP_AUTH_TOKEN"),
		QdrantAPIKey:      os.Getenv("QDRANT_API_KEY"),
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

// DocGenModel resolve o modelo de geração: override DOCGEN_MODEL, senão o
// default local validado no spike (qwen2.5-coder:7b).
func (c Config) DocGenModel() string {
	if c.DocGenModelOvr != "" {
		return c.DocGenModelOvr
	}
	return "qwen2.5-coder:7b"
}
