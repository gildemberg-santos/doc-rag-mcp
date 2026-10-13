package main

// docgen: gera documentação por arquivo via LLM e persiste na coleção
// Qdrant "-filedocs" (incremental por source_hash) + opcionalmente espelha
// .md em disco.
//
// Uso:
//
//	go run ./cmd/docgen --project demo --path ./internal/indexerstatus --provider ollama
//	go run ./cmd/docgen --all --provider ollama --out ./docs-geradas
//	go run ./cmd/docgen --all --provider ollama --store=false --out ./docs-geradas  (só markdown)
//	go run ./cmd/docgen --all --dry-run  (conta o que seria gerado, sem LLM/Qdrant writes)
import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"doc-rag-mcp/internal/config"
	"doc-rag-mcp/internal/docgen"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/filedocs"
	"doc-rag-mcp/internal/indexer"
	"doc-rag-mcp/internal/vector"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	cfg := config.Load()

	project := flag.String("project", "", "nome do projeto (default: nome da pasta)")
	path := flag.String("path", "", "caminho do projeto (default: $PROJECTS_ROOT/<project>)")
	all := flag.Bool("all", false, "documenta todas as subpastas de PROJECTS_ROOT")
	providerFlag := flag.String("provider", "", "provider de GERAÇÃO: só ollama (default: $DOCGEN_PROVIDER, $EMBED_PROVIDER ou ollama)")
	modelFlag := flag.String("model", "", "modelo de geração (default: $DOCGEN_MODEL ou qwen2.5-coder:7b)")
	embedProviderFlag := flag.String("embed-provider", "", "provider de EMBEDDING da doc: só ollama (default: segue o de embedding; precisa bater com o server)")
	out := flag.String("out", "", "diretório de saída (espelha <projeto>/<path>.md); vazio = imprime no stdout")
	store := flag.Bool("store", true, "persiste as docs na coleção Qdrant -filedocs (false = só markdown/stdout)")
	clean := flag.Bool("clean", false, "apaga as docs do projeto antes de regerar tudo")
	dryRun := flag.Bool("dry-run", false, "simula sem chamar o LLM nem escrever nada: só conta o que seria gerado")
	maxBytes := flag.Int("max-bytes", docgen.DefaultMaxBytes, "arquivos maiores que isso são pulados (não cabem no contexto local)")
	minLines := flag.Int("min-lines", 20, "arquivos com menos linhas são pulados (código já autoexplicativo)")
	concurrency := flag.Int("concurrency", 2, "gerações em paralelo (completions são pesadas; default 2)")
	flag.Parse()

	provider, err := embed.NormalizeProvider(firstNonEmpty(*providerFlag, os.Getenv("DOCGEN_PROVIDER"), cfg.EmbedProvider))
	if err != nil {
		log.Fatal(err)
	}
	model := *modelFlag
	if model == "" {
		model = cfg.DocGenModel()
	}

	g := docgen.NewOllama(cfg.OllamaURL, model, docgen.DefaultNumCtx, docgen.DefaultNumPredict, docgen.DefaultTemperature)

	// Embedder da doc (vetor do ponto = embedding da prosa): precisa usar os
	// mesmos dims do server, senão a busca não encontra nada. Necessário
	// sempre que for persistir — inclusive no dry-run com store (o dry-run
	// faz o diff contra o Qdrant, só não chama o LLM nem escreve).
	// Com --store=false o Qdrant nem é tocado (só markdown/stdout).
	var e embed.Provider
	var qDocs *vector.Client
	if *store {
		if _, err := embed.NormalizeProvider(firstNonEmpty(*embedProviderFlag, cfg.EmbedProvider, "")); err != nil {
			log.Fatal(err)
		}
		base := cfg.Collection
		e = embed.NewOllama(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaDims)
		qDocs = vector.New(cfg.QdrantURL, filedocs.CollectionFor(base))
	}
	fmt.Printf("📝 doc-rag docgen — geração=%s (%s) — saída: %s — store=%v\n", provider, model, outDir(*out), *store)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	targets := resolveTargets(cfg, *all, *project, *path)
	if len(targets) == 0 {
		log.Fatal("nenhum projeto para documentar (use --project/--path ou --all)")
	}

	var failed, doneFiles, doneDocs int
	for _, tg := range targets {
		st, err := filedocs.Run(ctx, tg.name, tg.dir, qDocs, e, g, filedocs.Options{
			Clean: *clean, DryRun: *dryRun, Store: *store,
			OutDir: *out, PrintStdout: *out == "",
			MaxBytes: *maxBytes, MinLines: *minLines,
			Concurrency: *concurrency, Model: model,
		})
		if err != nil {
			log.Printf("😿 [%s]: %v", tg.name, err)
			failed++
			continue
		}
		doneFiles += st.Files
		doneDocs += st.Generated
	}
	fmt.Printf("📝 ─── total: %d arquivos, %d documentados, %d falhas ───\n", doneFiles, doneDocs, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

type target struct {
	name string
	dir  string
}

func outDir(out string) string {
	if out == "" {
		return "stdout"
	}
	return out
}

// resolveTargets espelha a resolução do indexer: --all redescobre a cada
// execução; single-project deriva path de PROJECTS_ROOT.
func resolveTargets(cfg config.Config, all bool, project, path string) []target {
	if all {
		projects, err := indexer.DiscoverProjects(cfg.ProjectsRoot)
		if err != nil {
			log.Fatalf("discover: %v", err)
		}
		var out []target
		for _, p := range projects {
			out = append(out, target{name: p, dir: indexer.ProjectDir(cfg.ProjectsRoot, p)})
		}
		return out
	}
	if path == "" && project != "" {
		path = indexer.ProjectDir(cfg.ProjectsRoot, project)
	}
	if path == "" {
		path = cfg.ProjectsRoot
	}
	if project == "" {
		if env := os.Getenv("PROJECT_NAME"); env != "" {
			project = env
		} else if projects, _ := indexer.DiscoverProjects(path); len(projects) == 1 {
			project = projects[0]
		} else {
			project = "default"
		}
	}
	return []target{{name: project, dir: path}}
}

func firstNonEmpty(a, b, c string) string {
	if a != "" {
		return a
	}
	if b != "" {
		return b
	}
	return c
}
