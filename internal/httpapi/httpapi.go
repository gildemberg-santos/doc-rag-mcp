package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/search"
	"doc-rag-mcp/internal/vector"
)

// Handler expõe endpoints auxiliares de debug ao lado do /mcp.
type Handler struct {
	Qdrant   *vector.Client
	Embedder embed.Provider

	statusMu    sync.Mutex
	statusCache *StatusResponse
	statusAt    time.Time
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ProjectStat é a contagem de um projeto no índice.
type ProjectStat struct {
	Project   string `json:"project"`
	Chunks    int    `json:"chunks"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// StatusResponse é o payload JSON de /status — base pro dashboard vivo.
type StatusResponse struct {
	OK         bool          `json:"ok"`
	CheckedAt  string        `json:"checked_at"`
	QdrantURL  string        `json:"qdrant_url"`
	Collection string        `json:"collection"`
	Provider   string        `json:"provider"`
	Dims       int           `json:"dims"`
	Points     int64         `json:"points"`
	Projects   []ProjectStat `json:"projects"`
	Error      string        `json:"error,omitempty"`
}

// statusCacheTTL evita que um dashboard com auto-refresh agressivo (ou
// várias abas abertas) disparem um scroll completo da coleção no Qdrant a
// cada poucos segundos — o scroll é barato por página, mas uma coleção
// grande (dezenas de milhares de pontos) ainda custa centenas de ms.
const statusCacheTTL = 5 * time.Second

// Status reporta o estado do índice: GET /status (JSON).
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	h.statusMu.Lock()
	if h.statusCache != nil && time.Since(h.statusAt) < statusCacheTTL {
		cached := *h.statusCache
		h.statusMu.Unlock()
		_ = json.NewEncoder(w).Encode(cached)
		return
	}
	h.statusMu.Unlock()

	resp := h.computeStatus(r.Context())

	h.statusMu.Lock()
	h.statusCache = &resp
	h.statusAt = time.Now()
	h.statusMu.Unlock()

	if !resp.OK {
		w.WriteHeader(http.StatusBadGateway)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) computeStatus(ctx context.Context) StatusResponse {
	now := time.Now().UTC().Format(time.RFC3339)
	count, err := h.Qdrant.Count(ctx)
	if err != nil {
		return StatusResponse{OK: false, CheckedAt: now, Error: err.Error()}
	}

	counts := map[string]int{}
	fresh := map[string]string{}
	var offset any
	for {
		pts, next, err := h.Qdrant.ScrollFields(ctx, nil, 500, offset, []string{"project", "indexed_at"})
		if err != nil {
			return StatusResponse{OK: false, CheckedAt: now, Error: err.Error()}
		}
		for _, p := range pts {
			proj, _ := p.Payload["project"].(string)
			if proj == "" {
				continue
			}
			counts[proj]++
			if ts, _ := p.Payload["indexed_at"].(string); ts > fresh[proj] {
				fresh[proj] = ts
			}
		}
		if next == nil {
			break
		}
		offset = next
	}

	projects := make([]ProjectStat, 0, len(counts))
	for name, n := range counts {
		projects = append(projects, ProjectStat{Project: name, Chunks: n, UpdatedAt: fresh[name]})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Project < projects[j].Project })

	dims := 0
	provider := ""
	if h.Embedder != nil {
		dims = h.Embedder.Dims()
		provider = h.Embedder.Name()
	}

	return StatusResponse{
		OK:         true,
		CheckedAt:  now,
		QdrantURL:  h.Qdrant.BaseURL,
		Collection: h.Qdrant.Collection,
		Provider:   provider,
		Dims:       dims,
		Points:     count,
		Projects:   projects,
	}
}

// Dashboard serve uma página HTML que consome /status (mesma origem, sem
// CORS) e se atualiza sozinha — GET /dashboard.
func (h *Handler) Dashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dashboardHTML))
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
