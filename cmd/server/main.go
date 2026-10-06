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

	"doc-rag-mcp/internal/config"
	"doc-rag-mcp/internal/dockerstat"
	"doc-rag-mcp/internal/embed"
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
	providerFlag := flag.String("provider", "", "openai|ollama (default: $EMBED_PROVIDER ou openai)")
	flag.Parse()

	cfg := config.Load()
	if *httpAddr == "" {
		*httpAddr = ":" + cfg.HTTPPort
	}

	provider, err := embed.NormalizeProvider(firstNonEmpty(*providerFlag, cfg.EmbedProvider))
	if err != nil {
		log.Fatal(err)
	}
	collection := embed.CollectionFor(provider, cfg.Collection)

	var e embed.Provider
	if provider == embed.ProviderOllama {
		e = embed.NewOllama(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaDims)
	} else {
		if cfg.OpenAIAPIKey == "" {
			log.Fatal("OPENAI_API_KEY não configurada (veja .env.example) — ou use --provider ollama / EMBED_PROVIDER=ollama")
		}
		e = embed.NewWithDims(cfg.OpenAIAPIKey, cfg.OpenAIEmbedModel, cfg.EmbedDims)
	}
	q := vector.New(cfg.QdrantURL, collection)
	log.Printf("[%s] provider=%s coleção=%q dims=%d", cfg.MCPName, provider, collection, e.Dims())

	srv := mcpserver.New(cfg.MCPName, cfg.MCPVersion, &mcpserver.Deps{Qdrant: q, Embedder: e, Provider: provider, Dims: e.Dims()})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		log.Printf("[%s] MCP em modo stdio", cfg.MCPName)
		if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("stdio: %v", err)
		}
	case "http":
		runHTTP(ctx, *httpAddr, srv, q, e, cfg)
	case "both":
		// HTTP em background + stdio em foreground (para Claude Code + remoto ao mesmo tempo)
		go func() {
			if err := runHTTP(context.Background(), *httpAddr, srv, q, e, cfg); err != nil {
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func runHTTP(ctx context.Context, addr string, srv *mcp.Server, q *vector.Client, e embed.Provider, cfg config.Config) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return srv
	}, nil)
	aux := httpapi.New(q, e)
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

	httpSrv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()
	log.Printf("MCP Streamable HTTP em http://%s/mcp (health /health, debug /search, status /status, dashboard /dashboard)", addr)
	return httpSrv.ListenAndServe()
}
