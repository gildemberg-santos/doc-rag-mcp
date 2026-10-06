package config

import "testing"

// clearEnv simula "nenhuma env var setada" pras que Load() lê — t.Setenv
// com string vazia funciona porque getenv()/Load() tratam "" como ausente,
// e t.Setenv já restaura o valor original no fim do teste.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"QDRANT_URL", "QDRANT_COLLECTION", "EMBED_PROVIDER", "OPENAI_API_KEY",
		"OPENAI_EMBED_MODEL", "OPENAI_EMBED_DIMS", "OLLAMA_URL", "OLLAMA_MODEL",
		"OLLAMA_EMBED_DIMS", "PROJECTS_ROOT", "HTTP_PORT", "MCP_NAME", "MCP_VERSION",
		"INDEXER_STATUS_FILE", "DOCKER_SOCKET", "COMPOSE_PROJECT_NAME", "STATUS_HISTORY_FILE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg := Load()
	want := Config{
		QdrantURL:        "http://localhost:6333",
		Collection:       "docs",
		EmbedProvider:    "openai",
		OpenAIAPIKey:     "",
		OpenAIEmbedModel: "text-embedding-3-small",
		EmbedDims:        1536,
		OllamaURL:        "http://localhost:11434",
		OllamaModel:      "nomic-embed-text",
		OllamaDims:       768,
		ProjectsRoot:     "/projects",
		HTTPPort:         "8080",
		MCPName:          "doc-rag-mcp",
		MCPVersion:       "1.0.0",
		// Nível 2/3: vazios por default = seções omitidas no /status.
		IndexerStatusFile: "",
		DockerSocket:      "",
		ComposeProject:    "",
		HistoryFile:       "",
	}
	if cfg != want {
		t.Fatalf("defaults errados:\n got  %+v\n want %+v", cfg, want)
	}
}

func TestLoadLargeModelDefaultsTo3072Dims(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENAI_EMBED_MODEL", "text-embedding-3-large")
	cfg := Load()
	if cfg.EmbedDims != 3072 {
		t.Fatalf("text-embedding-3-large deveria default pra 3072 dims, veio %d", cfg.EmbedDims)
	}
}

func TestLoadExplicitDimsOverridesLargeModelDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENAI_EMBED_MODEL", "text-embedding-3-large")
	t.Setenv("OPENAI_EMBED_DIMS", "999")
	cfg := Load()
	if cfg.EmbedDims != 999 {
		t.Fatalf("OPENAI_EMBED_DIMS explícito deveria prevalecer, veio %d", cfg.EmbedDims)
	}
}

func TestLoadOllamaDimsFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("OLLAMA_EMBED_DIMS", "1024")
	cfg := Load()
	if cfg.OllamaDims != 1024 {
		t.Fatalf("esperava OllamaDims=1024, veio %d", cfg.OllamaDims)
	}
}

func TestLoadOllamaDimsInvalidFallsBackToDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("OLLAMA_EMBED_DIMS", "não-é-número")
	cfg := Load()
	if cfg.OllamaDims != 768 {
		t.Fatalf("valor inválido deveria cair no default 768, veio %d", cfg.OllamaDims)
	}
}

func TestLoadCustomValuesOverrideDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("QDRANT_URL", "http://qdrant:6333")
	t.Setenv("EMBED_PROVIDER", "ollama")
	t.Setenv("PROJECTS_ROOT", "/meus-projetos")
	cfg := Load()
	if cfg.QdrantURL != "http://qdrant:6333" || cfg.EmbedProvider != "ollama" || cfg.ProjectsRoot != "/meus-projetos" {
		t.Fatalf("valores customizados não foram respeitados: %+v", cfg)
	}
}

func TestLoadObservabilityEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("INDEXER_STATUS_FILE", "/state/indexer-status.json")
	t.Setenv("DOCKER_SOCKET", "/var/run/docker.sock")
	t.Setenv("COMPOSE_PROJECT_NAME", "meu-projeto")
	t.Setenv("STATUS_HISTORY_FILE", "/state/history.jsonl")
	cfg := Load()
	if cfg.IndexerStatusFile != "/state/indexer-status.json" {
		t.Errorf("INDEXER_STATUS_FILE não lido: %q", cfg.IndexerStatusFile)
	}
	if cfg.DockerSocket != "/var/run/docker.sock" {
		t.Errorf("DOCKER_SOCKET não lido: %q", cfg.DockerSocket)
	}
	if cfg.ComposeProject != "meu-projeto" {
		t.Errorf("COMPOSE_PROJECT_NAME não lido: %q", cfg.ComposeProject)
	}
	if cfg.HistoryFile != "/state/history.jsonl" {
		t.Errorf("STATUS_HISTORY_FILE não lido: %q", cfg.HistoryFile)
	}
}
