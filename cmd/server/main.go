package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"doc-rag-mcp/internal/clix"
	"doc-rag-mcp/internal/config"
	"doc-rag-mcp/internal/dockerstat"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/filedocs"
	"doc-rag-mcp/internal/history"
	"doc-rag-mcp/internal/httpapi"
	"doc-rag-mcp/internal/mcpserver"
	"doc-rag-mcp/internal/vector"

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	_ = godotenv.Load()

	transport := flag.String("transport", "http", "transporte MCP: stdio | http | both")
	httpAddr := flag.String("http-addr", "", "endereço HTTP (default :$HTTP_PORT)")
	providerFlag := flag.String("provider", "", "provider de embedding: só ollama (default: $EMBED_PROVIDER ou ollama)")
	flag.Parse()

	cfg := config.Load()
	if *httpAddr == "" {
		*httpAddr = ":" + cfg.HTTPPort
	}

	provider, err := embed.NormalizeProvider(clix.FirstNonEmpty(*providerFlag, cfg.EmbedProvider))
	if err != nil {
		log.Fatal(err)
	}
	collection := cfg.Collection

	e := embed.NewOllama(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaDims)
	q := vector.New(cfg.QdrantURL, collection)
	if cfg.QdrantAPIKey != "" {
		q = q.WithAPIKey(cfg.QdrantAPIKey)
	}
	log.Printf("[%s] provider=%s (%s) coleção=%q dims=%d", cfg.MCPName, provider, cfg.OllamaModel, collection, e.Dims())

	// Checagem de dependências no boot (não-fatal): Qdrant fora ou modelo
	// ausente no Ollama só quebrariam na primeira busca com erro genérico.
	// Aqui o aviso já diz o comando exato de correção.
	for _, w := range checkDependencies(context.Background(), q, e, cfg) {
		log.Printf("⚠️ %s", w)
	}

	// Coleção de documentação gerada (best-effort): se o Qdrant estiver
	// vazio de filedocs, as tools get_file_doc/search_file_docs avisam em
	// vez de quebrar — o resto do MCP funciona normalmente.
	fileDocs := q.WithCollection(filedocs.CollectionFor(collection))

	srv := mcpserver.New(cfg.MCPName, cfg.MCPVersion, &mcpserver.Deps{Qdrant: q, Embedder: e, Provider: provider, Dims: e.Dims(), FileDocs: fileDocs})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		log.Printf("[%s] MCP em modo stdio", cfg.MCPName)
		if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("stdio: %v", err)
		}
	case "http":
		runHTTP(ctx, *httpAddr, srv, q, fileDocs, e, cfg)
	case "both":
		// HTTP em background + stdio em foreground (para Claude Code + remoto ao mesmo tempo)
		go func() {
			if err := runHTTP(context.Background(), *httpAddr, srv, q, fileDocs, e, cfg); err != nil {
				log.Printf("http: %v", err)
			}
		}()
		log.Printf("[%s] MCP em modo both (http %s + stdio)", cfg.MCPName, *httpAddr)
		if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("stdio: %v", err)
		}
	default:
		fmt.Fprintln(os.Stderr, "transport deve ser stdio|http|both")
		os.Exit(1)
	}
}

// checkDependencies pinga Qdrant (count barato) e o Ollama (modelos)
// com timeout curto e devolve avisos acionáveis. Não-fatal de propósito:
// com restart-unless-stopped + ollama ainda subindo, falhar aqui seria
// crashloop; os erros reais continuam nas tools com 502.
func checkDependencies(ctx context.Context, q *vector.Client, e embed.Provider, cfg config.Config) []string {
	var warns []string
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := q.Count(pingCtx); err != nil {
		warns = append(warns, fmt.Sprintf("qdrant inacessível em %s: %v (suba com: docker compose up -d qdrant)", cfg.QdrantURL, err))
	}
	if pinger, ok := e.(embed.Pinger); ok {
		if err := pinger.Ping(pingCtx); err != nil {
			warns = append(warns, fmt.Sprintf("embedding indisponível: %v (confira OLLAMA_URL=%s e rode: ollama pull %s)", err, cfg.OllamaURL, cfg.OllamaModel))
		}
	}
	return warns
}

func runHTTP(ctx context.Context, addr string, srv *mcp.Server, q *vector.Client, fileDocs *vector.Client, e embed.Provider, cfg config.Config) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return srv
	}, nil)
	aux := httpapi.New(q, e)
	aux.FileDocs = fileDocs
	aux.ServerName = cfg.MCPName
	aux.ServerVersion = cfg.MCPVersion
	aux.AuthToken = cfg.AuthToken
	if cfg.AuthToken == "" {
		log.Printf("aviso: MCP_AUTH_TOKEN vazio — /mcp, /search, /status e /dashboard sem autenticação (ok em rede confiável/localhost)")
	}
	aux.IndexerStatusFile = cfg.IndexerStatusFile
	aux.History = history.New(cfg.HistoryFile)
	if cfg.DockerSocket != "" {
		aux.Docker = dockerstat.New(cfg.DockerSocket)
		aux.ComposeProject = cfg.ComposeProject
		log.Printf("docker-stat: socket=%s projeto=%q (opt-in, só leitura)", cfg.DockerSocket, cfg.ComposeProject)
	}
	if aux.IndexerStatusFile != "" {
		log.Printf("indexer-status: lendo heartbeat de %s", aux.IndexerStatusFile)
	}
	if cfg.HistoryFile != "" {
		log.Printf("status-history: persistindo série em %s", cfg.HistoryFile)
	}

	mux := http.NewServeMux()
	// Métricas de uso (Nível 1): contam /mcp (tools reais) + /search
	// (debug REST). /status, /dashboard e /health ficam de fora — são
	// observabilidade, não uso.
	mux.Handle("/mcp", httpapi.Metrics(aux, mcpHandler))
	mux.Handle("/search", httpapi.Metrics(aux, http.HandlerFunc(aux.Search)))
	mux.HandleFunc("/health", aux.Health)
	mux.HandleFunc("/status", aux.Status)
	mux.HandleFunc("/status/history", aux.HistoryHandler)
	mux.HandleFunc("/dashboard", aux.Dashboard)

	httpSrv := &http.Server{Addr: addr, Handler: httpapi.RequireAuth(aux, mux)}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()
	log.Printf("MCP Streamable HTTP em http://%s/mcp (health /health, debug /search, status /status, dashboard /dashboard)", addr)
	return httpSrv.ListenAndServe()
}
