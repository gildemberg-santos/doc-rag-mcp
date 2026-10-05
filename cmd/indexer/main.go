package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"doc-rag-mcp/internal/config"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/indexer"
	"doc-rag-mcp/internal/vector"

	"github.com/joho/godotenv"
)

// Uso:
//   go run ./cmd/indexer --project neurolead --path /projects/neurolead
//   go run ./cmd/indexer --all  (indexa todas as subpastas de PROJECTS_ROOT)
//   go run ./cmd/indexer --all --provider ollama  (índice local, coleção docs-ollama)
//   go run ./cmd/indexer --all --no-clean  (incremental: reembeda só chunks novos/
//     alterados por hash de conteúdo, remove órfãos — não apaga tudo antes)
func main() {
	_ = godotenv.Load()
	cfg := config.Load()

	project := flag.String("project", "", "nome do projeto (default: nome da pasta)")
	path := flag.String("path", "", "caminho do projeto (default: $PROJECTS_ROOT/<project>)")
	all := flag.Bool("all", false, "indexa todas as subpastas de PROJECTS_ROOT")
	noClean := flag.Bool("no-clean", false, "indexação incremental: só reembeda chunks novos/alterados (por hash de conteúdo) e remove pontos obsoletos; não apaga tudo antes (default: reindex completo)")
	providerFlag := flag.String("provider", "", "openai|ollama (default: $EMBED_PROVIDER ou openai)")
	flag.Parse()

	t0 := time.Now()

	provider, err := embed.NormalizeProvider(firstNonEmpty(*providerFlag, cfg.EmbedProvider))
	if err != nil {
		log.Fatal(err)
	}

	var e embed.Provider
	collection := embed.CollectionFor(provider, cfg.Collection)
	if provider == embed.ProviderOllama {
		e = embed.NewOllama(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaDims)
	} else {
		if cfg.OpenAIAPIKey == "" {
			log.Fatal("OPENAI_API_KEY não configurada (veja .env.example) — ou use --provider ollama")
		}
		e = embed.NewWithDims(cfg.OpenAIAPIKey, cfg.OpenAIEmbedModel, cfg.EmbedDims)
	}
	mode := "reindex 🔄"
	if *noClean {
		mode = "incremental ➕"
	}
	model := cfg.OpenAIEmbedModel
	if provider == embed.ProviderOllama {
		model = cfg.OllamaModel
	}
	fmt.Printf("🐱 doc-rag indexer — provider=%s (%s, %d dims) → coleção %q — modo %s\n",
		provider, model, e.Dims(), collection, mode)

	ctx := context.Background()
	q := vector.New(cfg.QdrantURL, collection)

	type result struct {
		project string
		chunks  int
		took    time.Duration
		err     error
	}
	var results []result

	runOne := func(p, dir string) {
		tp := time.Now()
		n, err := indexer.RunWithOptions(ctx, p, dir, q, e, indexer.Options{Clean: !*noClean})
		results = append(results, result{project: p, chunks: n, took: time.Since(tp).Round(time.Millisecond), err: err})
	}

	if *all {
		projects, err := indexer.DiscoverProjects(cfg.ProjectsRoot)
		if err != nil {
			log.Fatalf("discover: %v", err)
		}
		fmt.Printf("🐾 %d projeto(s) encontrado(s) em %s\n", len(projects), cfg.ProjectsRoot)
		for _, p := range projects {
			runOne(p, indexer.ProjectDir(cfg.ProjectsRoot, p))
		}
	} else {
		if *path == "" && *project != "" {
			*path = indexer.ProjectDir(cfg.ProjectsRoot, *project)
		}
		if *path == "" {
			*path = cfg.ProjectsRoot
		}
		if *project == "" {
			// deriva do path ou usa env PROJECT_NAME
			if env := os.Getenv("PROJECT_NAME"); env != "" {
				*project = env
			} else {
				projects, _ := indexer.DiscoverProjects(*path)
				if len(projects) == 1 {
					*project = projects[0]
				} else {
					*project = "default"
				}
			}
		}
		runOne(*project, *path)
	}

	// Resumo final com tempo e gatinhos 🐱
	fmt.Println()
	fmt.Println("🐱 ─── resumo ───")
	total := 0
	fails := 0
	for _, r := range results {
		if r.err != nil {
			fails++
			fmt.Printf("   😿 %-20s FALHOU em %s: %v\n", r.project, r.took, r.err)
			continue
		}
		total += r.chunks
		fmt.Printf("   ✅ %-20s %6d chunks em %s\n", r.project, r.chunks, r.took)
	}
	fmt.Printf("🐱 ─── total: %d chunks em %s (%d ok, %d falhas) ─── 🐱\n",
		total, time.Since(t0).Round(time.Millisecond), len(results)-fails, fails)
	if fails > 0 {
		os.Exit(1)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
