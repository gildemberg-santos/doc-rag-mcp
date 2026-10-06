package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"doc-rag-mcp/internal/config"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/indexer"
	"doc-rag-mcp/internal/indexerstatus"
	"doc-rag-mcp/internal/vector"

	"github.com/joho/godotenv"
)

// Uso:
//
//	go run ./cmd/indexer --project neurolead --path /projects/neurolead
//	go run ./cmd/indexer --all  (indexa todas as subpastas de PROJECTS_ROOT)
//	go run ./cmd/indexer --all --provider ollama  (índice local, coleção docs-ollama)
//	go run ./cmd/indexer --all --no-clean  (incremental: reembeda só chunks novos/
//	  alterados por hash de conteúdo, remove órfãos — não apaga tudo antes)
//	go run ./cmd/indexer --all --batch-size 16  (lotes menores de embed+upsert:
//	  menos memória/menos trabalho perdido se falhar no meio; default 64)
//	go run ./cmd/indexer --all --watch --interval 10m  (repete a indexação
//	  incremental a cada intervalo; Ctrl-C/SIGTERM encerra de forma limpa)
func main() {
	_ = godotenv.Load()
	cfg := config.Load()

	project := flag.String("project", "", "nome do projeto (default: nome da pasta)")
	path := flag.String("path", "", "caminho do projeto (default: $PROJECTS_ROOT/<project>)")
	all := flag.Bool("all", false, "indexa todas as subpastas de PROJECTS_ROOT")
	noClean := flag.Bool("no-clean", false, "indexação incremental: só reembeda chunks novos/alterados (por hash de conteúdo) e remove pontos obsoletos; não apaga tudo antes (default: reindex completo)")
	providerFlag := flag.String("provider", "", "openai|ollama (default: $EMBED_PROVIDER ou openai)")
	batchSize := flag.Int("batch-size", 64, "tamanho do lote de embed+upsert (streaming: controla memória e quanto se perde se falhar no meio)")
	watch := flag.Bool("watch", false, "modo contínuo: repete a indexação incremental a cada --interval, até Ctrl-C/SIGTERM (ignora --no-clean=false — força incremental em todo ciclo)")
	interval := flag.Duration("interval", 10*time.Minute, "intervalo entre ciclos no modo --watch (ex.: 5m, 30s)")
	statusFile := flag.String("status-file", os.Getenv("INDEXER_STATUS_FILE"), "caminho pra gravar um heartbeat JSON após cada ciclo (default: $INDEXER_STATUS_FILE, vazio desliga) — pra outro processo (ex. mcp-server) ler o status do --watch sem acesso a este processo")
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
	clean := !*noClean
	if *watch && clean {
		fmt.Println("🐾 --watch força indexação incremental em todo ciclo (reindex completo a cada ciclo não faria sentido) — ignorando --no-clean=false")
		clean = false
	}
	mode := "reindex 🔄"
	if !clean {
		mode = "incremental ➕"
	}
	model := cfg.OpenAIEmbedModel
	if provider == embed.ProviderOllama {
		model = cfg.OllamaModel
	}
	watchDesc := ""
	if *watch {
		watchDesc = fmt.Sprintf(" — watch a cada %s", *interval)
	}
	fmt.Printf("🐱 doc-rag indexer — provider=%s (%s, %d dims) → coleção %q — modo %s%s\n",
		provider, model, e.Dims(), collection, mode, watchDesc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	q := vector.New(cfg.QdrantURL, collection)

	if *statusFile != "" {
		if err := indexerstatus.EnsureDir(*statusFile); err != nil {
			log.Printf("🐾 aviso: não consegui preparar o diretório de %s: %v", *statusFile, err)
		}
	}

	// Resolve o alvo single-project uma vez (não muda entre ciclos). O modo
	// --all redescobre projetos a cada ciclo, pra pegar pastas novas.
	if !*all {
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
	}

	type result struct {
		project string
		chunks  int
		took    time.Duration
		err     error
	}

	// runCycle roda uma passada completa sobre todos os alvos e imprime o
	// resumo. Retorna o nº de falhas reais (cancelamento de contexto não
	// conta como falha) e se o ciclo foi interrompido por sinal.
	runCycle := func(cycleStart time.Time) (fails int, canceled bool) {
		var results []result
		runOne := func(p, dir string) bool {
			tp := time.Now()
			n, err := indexer.RunWithOptions(ctx, p, dir, q, e, indexer.Options{Clean: clean, EmbedBatchSize: *batchSize})
			results = append(results, result{project: p, chunks: n, took: time.Since(tp).Round(time.Millisecond), err: err})
			return err == nil || !errors.Is(err, context.Canceled)
		}

		if *all {
			projects, err := indexer.DiscoverProjects(cfg.ProjectsRoot)
			if err != nil {
				log.Fatalf("discover: %v", err)
			}
			fmt.Printf("🐾 %d projeto(s) encontrado(s) em %s\n", len(projects), cfg.ProjectsRoot)
			for _, p := range projects {
				if !runOne(p, indexer.ProjectDir(cfg.ProjectsRoot, p)) {
					break // contexto cancelado — não adianta tentar os próximos
				}
			}
		} else {
			runOne(*project, *path)
		}

		fmt.Println()
		fmt.Println("🐱 ─── resumo do ciclo ───")
		total := 0
		for _, r := range results {
			switch {
			case r.err != nil && errors.Is(r.err, context.Canceled):
				canceled = true
				fmt.Printf("   ⏹️  %-20s interrompido em %s\n", r.project, r.took)
			case r.err != nil:
				fails++
				fmt.Printf("   😿 %-20s FALHOU em %s: %v\n", r.project, r.took, r.err)
			default:
				total += r.chunks
				fmt.Printf("   ✅ %-20s %6d chunks em %s\n", r.project, r.chunks, r.took)
			}
		}
		fmt.Printf("🐱 ─── total: %d chunks em %s (%d ok, %d falhas) ─── 🐱\n",
			total, time.Since(cycleStart).Round(time.Millisecond), len(results)-fails, fails)

		if *statusFile != "" {
			st := indexerstatus.Status{
				UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
				IntervalSeconds: int(interval.Seconds()),
				CycleStartedAt:  cycleStart.UTC().Format(time.RFC3339),
				CycleDurationMs: time.Since(cycleStart).Milliseconds(),
				TotalChunks:     total,
				OKCount:         len(results) - fails,
				FailCount:       fails,
			}
			for _, r := range results {
				errMsg := ""
				if r.err != nil {
					errMsg = r.err.Error()
				}
				st.Projects = append(st.Projects, indexerstatus.ProjectCycle{
					Project: r.project, Chunks: r.chunks, DurationMs: r.took.Milliseconds(), Error: errMsg,
				})
			}
			if *watch && !canceled {
				st.NextCycleAt = time.Now().Add(*interval).UTC().Format(time.RFC3339)
			}
			if err := indexerstatus.Write(*statusFile, st); err != nil {
				log.Printf("🐾 aviso: não gravei o status em %s: %v", *statusFile, err)
			}
		}
		return fails, canceled
	}

	if !*watch {
		fails, _ := runCycle(t0)
		if fails > 0 {
			os.Exit(1)
		}
		return
	}

	for {
		cycleStart := time.Now()
		_, canceled := runCycle(cycleStart)
		if canceled || ctx.Err() != nil {
			fmt.Println("🐾 encerrado (sinal recebido) — até a próxima 🐱")
			return
		}
		fmt.Printf("🐾 próximo ciclo em %s...\n", *interval)
		select {
		case <-ctx.Done():
			fmt.Println("🐾 encerrado (sinal recebido) — até a próxima 🐱")
			return
		case <-time.After(*interval):
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
