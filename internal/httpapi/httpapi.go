package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/search"
	"doc-rag-mcp/internal/vector"
)

// Handler expõe endpoints auxiliares de debug ao lado do /mcp.
type Handler struct {
	Qdrant   *vector.Client
	Embedder embed.Provider
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Search é um endpoint REST simples para testar o RAG sem cliente MCP:
// GET /search?q=...&project=a,b&k=5&path_prefix=...&language=...&pure_vector=1&raw=1
// Mesma lógica do search_docs (pool + rerank + diversify), salvo raw=1.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "param ?q= obrigatório", http.StatusBadRequest)
		return
	}
	k := 5
	if ks := r.URL.Query().Get("k"); ks != "" {
		if n, err := strconv.Atoi(ks); err == nil && n > 0 && n <= 50 {
			k = n
		}
	}
	diversify := r.URL.Query().Get("diversify") != "0"
	pure := r.URL.Query().Get("pure_vector") == "1" || r.URL.Query().Get("raw") == "1"
	hits, err := search.Run(context.Background(), h.Qdrant, h.Embedder, search.Params{
		Query:      q,
		Projects:   search.ParseProjects(r.URL.Query().Get("project")),
		TopK:       k,
		Diversify:  diversify,
		PathPrefix: r.URL.Query().Get("path_prefix"),
		Language:   r.URL.Query().Get("language"),
		PureVector: pure,
	})
	if err != nil {
		http.Error(w, "search: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"query": q, "results": hits})
}
