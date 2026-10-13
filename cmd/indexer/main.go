package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"doc-rag-mcp/internal/config"
	"doc-rag-mcp/internal/docgen"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/filedocs"
	"doc-rag-mcp/internal/indexer"
	"doc-rag-mcp/internal/indexerstatus"
	"doc-rag-mcp/internal/vector"

	"github.com/joho/godotenv"
)

// Uso:
//
//	go run ./cmd/indexer --project neurolead --path /projects/neurolead
//	 (default: incremental — só reembeda chunks novos/alterados por hash de
//	  conteúdo e remove pontos obsoletos; nunca apaga tudo antes)
//	go run ./cmd/indexer --all  (indexa todas as subpastas de PROJECTS_ROOT)
//	go run ./cmd/indexer --all --provider ollama  (índice local, coleção docs-ollama)
//	go run ./cmd/indexer --all --clean  (reindex completo: apaga tudo do
//	  projeto antes e reembeda tudo — reservado para migrações de dims/modelo)
//	go run ./cmd/indexer --all --batch-size 16  (lotes menores de embed+upsert:
//	  menos memória/menos trabalho perdido se falhar no meio; default 64)
//	go run ./cmd/indexer --all --embed-concurrency 4  (lotes de embed+upsert
//	  em paralelo; default 4, use 1 para sequencial)
//	go run ./cmd/indexer --all --dry-run  (simula o ciclo sem escrever nada:
//	  só conta inalterados/a-embedar/obsoletos)
//	go run ./cmd/indexer --all --watch --interval 10m  (repete a indexação
//	  incremental a cada intervalo; Ctrl-C/SIGTERM encerra de forma limpa)
//	go run ./cmd/indexer --all --generate-docs --docgen-provider ollama
//	  (gera docs por arquivo na coleção -filedocs; no --watch, repete a
//	  cada --docgen-interval, default 24h, e só p/ --docgen-projects)
func main() {
	_ = godotenv.Load()
	cfg := config.Load()

	project := flag.String("project", "", "nome do projeto (default: nome da pasta)")
	path := flag.String("path", "", "caminho do projeto (default: $PROJECTS_ROOT/<project>)")
	all := flag.Bool("all", false, "indexa todas as subpastas de PROJECTS_ROOT")
	cleanFlag := flag.Bool("clean", false, "reindex completo: apaga tudo do projeto antes e reembeda tudo — reservado para migrações de dims/modelo (default: incremental)")
	noClean := flag.Bool("no-clean", true, "DEPRECIADO (mantido por compatibilidade): o default agora já é incremental; --no-clean=false equivale a --clean")
	providerFlag := flag.String("provider", "", "provider de embedding: só ollama (default: $EMBED_PROVIDER ou ollama)")
	batchSize := flag.Int("batch-size", 64, "tamanho do lote de embed+upsert (streaming: controla memória e quanto se perde se falhar no meio)")
	embedConcurrency := flag.Int("embed-concurrency", 4, "lotes de embed+upsert em paralelo (default 4; use 1 para sequencial determinístico)")
	dryRun := flag.Bool("dry-run", false, "simula o ciclo sem escrever nada: não embbeda, não faz upsert, não deleta — só conta e loga o que seria feito")
	watch := flag.Bool("watch", false, "modo contínuo: repete a indexação incremental a cada --interval, até Ctrl-C/SIGTERM (ignora --no-clean=false — força incremental em todo ciclo)")
	interval := flag.Duration("interval", 10*time.Minute, "intervalo entre ciclos no modo --watch (ex.: 5m, 30s)")
	statusFile := flag.String("status-file", os.Getenv("INDEXER_STATUS_FILE"), "caminho pra gravar um heartbeat JSON após cada ciclo (default: $INDEXER_STATUS_FILE, vazio desliga) — pra outro processo (ex. mcp-server) ler o status do --watch sem acesso a este processo")
	genDocs := flag.Bool("generate-docs", os.Getenv("DOCGEN_ENABLED") == "true", "gera documentação por arquivo após indexar cada projeto (coleção -filedocs, incremental por source_hash; provider de geração independente via --docgen-provider; também liga com DOCGEN_ENABLED=true)")
	docgenProviderFlag := flag.String("docgen-provider", "", "provider de GERAÇÃO de docs: só ollama (default: $DOCGEN_PROVIDER, senão segue o de embedding)")
	docgenModelFlag := flag.String("docgen-model", "", "modelo de geração (default: $DOCGEN_MODEL ou qwen2.5-coder:7b)")
	docgenConcurrency := flag.Int("docgen-concurrency", 2, "gerações de doc em paralelo (completions pesadas; default 2)")
	docgenInterval := flag.Duration("docgen-interval", docgenIntervalDefault(), "intervalo mínimo entre ciclos de filedocs no modo --watch (completions são caras/lentas; default 24h ou $DOCGEN_INTERVAL; one-shot ignora)")
	docgenProjects := flag.String("docgen-projects", os.Getenv("DOCGEN_PROJECTS"), "só gera docs para estes projetos (csv; vazio = todos do ciclo; também via $DOCGEN_PROJECTS) — opt-in por projeto")
	flag.Parse()

	t0 := time.Now()

	provider, err := embed.NormalizeProvider(firstNonEmpty(*providerFlag, cfg.EmbedProvider))
	if err != nil {
		log.Fatal(err)
	}

	collection := cfg.Collection
	e := embed.NewOllama(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaDims)
	clean := resolveClean(*cleanFlag, *noClean, *watch)
	if *watch && clean {
		fmt.Println("🐾 --watch força indexação incremental em todo ciclo (reindex completo a cada ciclo não faria sentido) — ignorando --clean")
		clean = false
	}
	mode := "reindex 🔄"
	if *dryRun {
		mode = "dry-run 🧪"
	} else if !clean {
		mode = "incremental ➕"
	}
	model := cfg.OllamaModel
	watchDesc := ""
	if *watch {
		watchDesc = fmt.Sprintf(" — watch a cada %s", *interval)
	}
	dryDesc := ""
	if *dryRun {
		dryDesc = " — DRY-RUN (nada será escrito)"
	}
	fmt.Printf("🐱 doc-rag indexer — provider=%s (%s, %d dims) → coleção %q — modo %s%s%s\n",
		provider, model, e.Dims(), collection, mode, watchDesc, dryDesc)
	if *genDocs {
		scope := "todos os projetos"
		if *docgenProjects != "" {
			scope = "projetos: " + *docgenProjects
		}
		if *watch {
			fmt.Printf("📝 filedocs ligado — %s, a cada %s (1º ciclo + throttle)\n", scope, *docgenInterval)
		} else {
			fmt.Printf("📝 filedocs ligado — %s (one-shot, sem throttle)\n", scope)
		}
	}

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
	//
	// Filedocs (Fase 4): no modo --watch a etapa de docs é throttled pelo
	// --docgen-interval (completions são caras) e filtrada por
	// --docgen-projects; no one-shot, roda sempre que --generate-docs.
	var lastDocgen time.Time
	runCycle := func(cycleStart time.Time) (fails int, canceled bool) {
		doDocs := *genDocs
		if doDocs && *watch && !docgenDue(lastDocgen, cycleStart, *docgenInterval) {
			doDocs = false
			log.Printf("🐾 filedocs adiado (intervalo %s, próxima janela em %s) — chunks indexam normalmente",
				*docgenInterval, lastDocgen.Add(*docgenInterval).Format(time.RFC3339))
		}
		docFilter := parseDocgenProjects(*docgenProjects)
		var results []result
		runOne := func(p, dir string) bool {
			tp := time.Now()
			n, err := indexer.RunWithOptions(ctx, p, dir, q, e, indexer.Options{Clean: clean, EmbedBatchSize: *batchSize, EmbedConcurrency: *embedConcurrency, DryRun: *dryRun})
			results = append(results, result{project: p, chunks: n, took: time.Since(tp).Round(time.Millisecond), err: err})
			if err != nil {
				return !errors.Is(err, context.Canceled)
			}
			// Etapa extra opt-in: documentação por arquivo (coleção
			// -filedocs, incremental por source_hash). Roda depois do
			// embed+upsert do projeto, no mesmo ciclo (Opção A do plano).
			// Falha aqui não invalida os chunks já indexados acima.
			if doDocs && (len(docFilter) == 0 || docFilter[p]) {
				if derr := runFileDocs(ctx, cfg, q, e, p, dir, *docgenProviderFlag, *docgenModelFlag, *docgenConcurrency, clean, *dryRun); derr != nil {
					if errors.Is(derr, context.Canceled) {
						return false
					}
					log.Printf("😿 [%s] filedocs FALHOU (chunks ok): %v", p, derr)
				}
			}
			return true
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
		if doDocs && ctx.Err() == nil {
			lastDocgen = cycleStart
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

// runFileDocs executa a etapa opt-in de documentação por arquivo de um
// projeto (Opção A do plano): gera prosa via docgen.Provider independente e
// persiste na coleção -filedocs com o mesmo embedder do índice principal
// (o vetor do ponto é o embedding da doc, então precisa dos mesmos dims).
func runFileDocs(ctx context.Context, cfg config.Config, q *vector.Client, e embed.Provider, project, dir, docgenProviderFlag, docgenModelFlag string, docgenConcurrency int, clean, dryRun bool) error {
	if _, err := embed.NormalizeProvider(firstNonEmpty(docgenProviderFlag, firstNonEmpty(os.Getenv("DOCGEN_PROVIDER"), cfg.EmbedProvider))); err != nil {
		return err
	}
	model := docgenModelFlag
	if model == "" {
		model = cfg.DocGenModel()
	}
	g := docgen.NewOllama(cfg.OllamaURL, model, docgen.DefaultNumCtx, docgen.DefaultNumPredict, docgen.DefaultTemperature)
	qDocs := q.WithCollection(filedocs.CollectionFor(q.Collection))
	st, err := filedocs.Run(ctx, project, dir, qDocs, e, g, filedocs.Options{
		Clean: clean, DryRun: dryRun, Store: true,
		Concurrency: docgenConcurrency, Model: model,
	})
	if err != nil {
		return err
	}
	log.Printf("📝 [%s] filedocs: %d arquivos (%d gerados, %d com doc atual, %d removidos)",
		project, st.Files, st.Generated, st.Skipped, st.Removed)
	return nil
}

// resolveClean decide entre reindex completo e incremental.
// Default é incremental (false). --clean=true força completo (salvo --watch,
// tratado pelo chamador). --no-clean é o alias depreciado: --no-clean=false
// equivale a --clean (preserva o comportamento de quem passava a flag
// explicitamente quando o default ainda era reindex).
func resolveClean(cleanFlag, noCleanFlag, watch bool) bool {
	if watch {
		return false
	}
	return cleanFlag || !noCleanFlag
}

// docgenIntervalDefault resolve o intervalo da etapa filedocs no --watch:
// $DOCGEN_INTERVAL (ex.: "24h", "12h") quando válido, senão 24h (Fase 4 —
// completions são caras/lentas, docs não precisam do frescor do embedding).
func docgenIntervalDefault() time.Duration {
	if v := strings.TrimSpace(os.Getenv("DOCGEN_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 24 * time.Hour
}

// parseDocgenProjects converte o csv de --docgen-projects/$DOCGEN_PROJECTS
// em set de opt-in (nil/vazio = todos os projetos do ciclo).
func parseDocgenProjects(csv string) map[string]bool {
	var out map[string]bool
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			if out == nil {
				out = map[string]bool{}
			}
			out[p] = true
		}
	}
	return out
}

// docgenDue decide se a etapa filedocs roda neste ciclo do --watch:
// sempre no primeiro ciclo (last zero), depois só após o intervalo.
// Intervalo <=0 desliga o throttle (todo ciclo).
func docgenDue(last, now time.Time, interval time.Duration) bool {
	if last.IsZero() {
		return true
	}
	if interval <= 0 {
		return true
	}
	return !now.Before(last.Add(interval))
}
