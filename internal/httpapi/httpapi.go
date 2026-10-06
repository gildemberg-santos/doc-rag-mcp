package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"doc-rag-mcp/internal/dockerstat"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/history"
	"doc-rag-mcp/internal/indexerstatus"
	"doc-rag-mcp/internal/search"
	"doc-rag-mcp/internal/vector"
)

// Handler expõe endpoints auxiliares de debug ao lado do /mcp.
type Handler struct {
	Qdrant   *vector.Client
	Embedder embed.Provider

	// IndexerStatusFile, se setado, é lido em todo /status — heartbeat que
	// o indexer --watch grava após cada ciclo (ver internal/indexerstatus).
	// Vazio = seção omitida (deployment sem --watch, ou sem volume
	// compartilhado configurado).
	IndexerStatusFile string

	// Docker, se não-nil, é usado pra listar containers do mesmo projeto
	// docker-compose — ver internal/dockerstat. Nil = seção omitida.
	Docker         *dockerstat.Client
	ComposeProject string

	// History, se não-nil, recebe um Sample a cada /status real (não
	// servido do cache) e serve GET /status/history. Nil = sem histórico.
	History *history.Recorder

	startedAt time.Time

	reqCount     int64 // atomic
	reqLatencyUs int64 // atomic: soma de latências em microssegundos

	statusMu    sync.Mutex
	statusCache *StatusResponse
	statusAt    time.Time

	embedHealthMu    sync.Mutex
	embedHealthCache *EmbedHealth
	embedHealthAt    time.Time
}

// New constrói um Handler já com o relógio de uptime zerado.
func New(q *vector.Client, e embed.Provider) *Handler {
	return &Handler{Qdrant: q, Embedder: e, startedAt: time.Now(), History: history.New("")}
}

// TrackRequest soma uma requisição + sua duração às métricas de uso
// (ver Metrics, o middleware que chama isso). Seguro pra chamar de
// múltiplas goroutines concorrentes.
func (h *Handler) TrackRequest(d time.Duration) {
	atomic.AddInt64(&h.reqCount, 1)
	atomic.AddInt64(&h.reqLatencyUs, d.Microseconds())
}

// Metrics envolve um http.Handler contando requisições e latência média —
// usado em cima de /mcp (o caminho real de uso: search_docs, grep_docs...)
// pra alimentar as métricas de uso do /status.
func Metrics(h *Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		next.ServeHTTP(w, r)
		h.TrackRequest(time.Since(t0))
	})
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

// CollectionHealth resume status/config da coleção ativa no Qdrant.
type CollectionHealth struct {
	Status              string `json:"status"`
	OptimizerStatus     string `json:"optimizer_status"`
	PointsCount         int64  `json:"points_count"`
	IndexedVectorsCount int64  `json:"indexed_vectors_count"`
	SegmentsCount       int    `json:"segments_count"`
	VectorSize          int    `json:"vector_size"`
	Distance            string `json:"distance"`
}

// OtherCollection é uma coleção Qdrant que existe mas não é a ativa —
// tipicamente um índice órfão de uma config anterior (outro provider).
type OtherCollection struct {
	Name   string `json:"name"`
	Points int64  `json:"points"`
}

// EmbedHealth é o resultado do ping no provider de embedding (não gera
// nenhum embedding de verdade — ver embed.Pinger).
type EmbedHealth struct {
	Checked bool   `json:"checked"` // false = provider não implementa Pinger
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// SelfStats é o que o próprio processo mcp-server sabe sobre si mesmo —
// zero dependência nova, só runtime.
type SelfStats struct {
	UptimeSeconds int64   `json:"uptime_seconds"`
	Goroutines    int     `json:"goroutines"`
	HeapAllocMB   float64 `json:"heap_alloc_mb"`
	RequestsTotal int64   `json:"requests_total"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
}

// ContainerStat é um container do mesmo projeto docker-compose.
type ContainerStat struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`
	Status string `json:"status"`
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

	// Nível 1 — saúde detalhada, sem acesso novo nenhum.
	CollectionHealth *CollectionHealth `json:"collection_health,omitempty"`
	DimsMismatch     bool              `json:"dims_mismatch,omitempty"`
	OtherCollections []OtherCollection `json:"other_collections,omitempty"`
	EmbedHealth      *EmbedHealth      `json:"embed_health,omitempty"`
	Self             SelfStats         `json:"self"`

	// Nível 2 — status do indexer --watch via arquivo compartilhado.
	Indexer *indexerstatus.Status `json:"indexer,omitempty"`

	// Nível 3 — containers do projeto, via socket do Docker.
	Containers []ContainerStat `json:"containers,omitempty"`
}

// statusCacheTTL evita que um dashboard com auto-refresh agressivo (ou
// várias abas abertas) disparem um scroll completo da coleção no Qdrant a
// cada poucos segundos — o scroll é barato por página, mas uma coleção
// grande (dezenas de milhares de pontos) ainda custa centenas de ms.
const statusCacheTTL = 5 * time.Second

// embedHealthCacheTTL é mais longo que o do resto do /status: pingar o
// provider de embedding é mais caro (rede externa, no caso OpenAI tem
// rate-limit) — não precisa ser tão fresco quanto a contagem de pontos.
const embedHealthCacheTTL = 30 * time.Second

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

	h.recordHistory(resp)

	h.statusMu.Lock()
	h.statusCache = &resp
	h.statusAt = time.Now()
	h.statusMu.Unlock()

	if !resp.OK {
		w.WriteHeader(http.StatusBadGateway)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// HistoryHandler serve GET /status/history — últimos N samples da série
// temporal (query ?n=, default 120, máx 2000). Nunca falha por falta de
// dados: sem samples devolve {"samples":[]}.
func (h *Handler) HistoryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	n := 120
	if qs := r.URL.Query().Get("n"); qs != "" {
		if v, err := strconv.Atoi(qs); err == nil && v > 0 {
			n = v
		}
	}
	samples := []history.Sample{}
	if h.History != nil {
		samples = h.History.Last(n)
	}
	if samples == nil {
		samples = []history.Sample{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"samples": samples})
}

// recordHistory anexa um Sample a cada /status real. Nunca falha o
// request — histórico é best-effort.
func (h *Handler) recordHistory(resp StatusResponse) {
	if h.History == nil {
		return
	}
	self := resp.Self
	s := history.Sample{
		Timestamp:     resp.CheckedAt,
		Points:        resp.Points,
		RequestsTotal: self.RequestsTotal,
		AvgLatencyMs:  self.AvgLatencyMs,
		HeapAllocMB:   self.HeapAllocMB,
		Goroutines:    self.Goroutines,
		OK:            resp.OK,
	}
	if resp.EmbedHealth != nil && resp.EmbedHealth.Checked {
		ok := resp.EmbedHealth.OK
		s.EmbedOK = &ok
	}
	if resp.CollectionHealth != nil {
		ok := resp.CollectionHealth.Status == "green" && resp.CollectionHealth.OptimizerStatus == "ok"
		s.CollectionOK = &ok
	}
	// Falha total (Qdrant fora) também entra na série — é justamente o
	// que o gráfico precisa mostrar. Self vazio aqui significa Handler
	// construído na mão sem New(); selfStats() cobre.
	h.History.Append(s)
}

func (h *Handler) computeStatus(ctx context.Context) StatusResponse {
	now := time.Now().UTC().Format(time.RFC3339)
	// base carrega o contexto estático (útil mesmo quando o Qdrant está
	// fora — o dashboard mostra contra o que se tentou conectar).
	base := StatusResponse{CheckedAt: now, Self: h.selfStats()}
	if h.Qdrant != nil {
		base.QdrantURL = h.Qdrant.BaseURL
		base.Collection = h.Qdrant.Collection
	}
	if h.Embedder != nil {
		base.Dims = h.Embedder.Dims()
		base.Provider = h.Embedder.Name()
	}
	count, err := h.Qdrant.Count(ctx)
	if err != nil {
		base.Error = err.Error()
		return base
	}

	counts := map[string]int{}
	fresh := map[string]string{}
	var offset any
	for {
		pts, next, err := h.Qdrant.ScrollFields(ctx, nil, 500, offset, []string{"project", "indexed_at"})
		if err != nil {
			base.Error = err.Error()
			return base
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

	resp := base
	resp.OK = true
	resp.Points = count
	resp.Projects = projects
	dims := base.Dims

	// Nível 1: saúde da coleção + dims mismatch (o bug documentado no
	// README — trocar modelo muda dims, a coleção não recria sozinha —
	// vira um aviso ativo em vez de só texto na documentação).
	if info, err := h.Qdrant.CollectionInfo(ctx); err == nil {
		resp.CollectionHealth = &CollectionHealth{
			Status: info.Status, OptimizerStatus: info.OptimizerStatus,
			PointsCount: info.PointsCount, IndexedVectorsCount: info.IndexedVectorsCount,
			SegmentsCount: info.SegmentsCount, VectorSize: info.VectorSize, Distance: info.Distance,
		}
		if dims > 0 && info.VectorSize > 0 && dims != info.VectorSize {
			resp.DimsMismatch = true
		}
	}

	// Nível 1: coleções órfãs (índices de outra config, não usados mais).
	if names, err := h.Qdrant.ListCollectionNames(ctx); err == nil {
		for _, name := range names {
			if name == h.Qdrant.Collection {
				continue
			}
			n, err := h.Qdrant.CountFor(ctx, name)
			if err != nil {
				continue
			}
			resp.OtherCollections = append(resp.OtherCollections, OtherCollection{Name: name, Points: n})
		}
	}

	// Nível 1: embed provider realmente alcançável (não só o Qdrant).
	resp.EmbedHealth = h.embedHealth(ctx)

	// Nível 2: status do indexer --watch (heartbeat em arquivo).
	if h.IndexerStatusFile != "" {
		if st, err := indexerstatus.Read(h.IndexerStatusFile); err == nil {
			resp.Indexer = &st
		}
	}

	// Nível 3: containers do mesmo projeto docker-compose.
	if h.Docker != nil {
		if containers, err := h.Docker.ListContainers(ctx, h.ComposeProject); err == nil {
			for _, c := range containers {
				resp.Containers = append(resp.Containers, ContainerStat{
					Name: c.Name, Image: c.Image, State: c.State, Status: c.Status,
				})
			}
			sort.Slice(resp.Containers, func(i, j int) bool { return resp.Containers[i].Name < resp.Containers[j].Name })
		}
	}

	return resp
}

func (h *Handler) selfStats() SelfStats {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	count := atomic.LoadInt64(&h.reqCount)
	latencyUs := atomic.LoadInt64(&h.reqLatencyUs)
	avgMs := 0.0
	if count > 0 {
		avgMs = float64(latencyUs) / float64(count) / 1000.0
	}
	uptime := int64(0)
	if !h.startedAt.IsZero() {
		uptime = int64(time.Since(h.startedAt).Seconds())
	}
	return SelfStats{
		UptimeSeconds: uptime,
		Goroutines:    runtime.NumGoroutine(),
		HeapAllocMB:   float64(mem.HeapAlloc) / (1024 * 1024),
		RequestsTotal: count,
		AvgLatencyMs:  avgMs,
	}
}

// embedHealth pinga o provider de embedding, com cache próprio (mais
// longo que o resto do /status — ver embedHealthCacheTTL).
func (h *Handler) embedHealth(ctx context.Context) *EmbedHealth {
	h.embedHealthMu.Lock()
	if h.embedHealthCache != nil && time.Since(h.embedHealthAt) < embedHealthCacheTTL {
		cached := *h.embedHealthCache
		h.embedHealthMu.Unlock()
		return &cached
	}
	h.embedHealthMu.Unlock()

	result := &EmbedHealth{Checked: false, OK: true}
	if pinger, ok := h.Embedder.(embed.Pinger); ok {
		result.Checked = true
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := pinger.Ping(pingCtx); err != nil {
			result.OK = false
			result.Error = err.Error()
		}
	}

	h.embedHealthMu.Lock()
	cached := *result
	h.embedHealthCache = &cached
	h.embedHealthAt = time.Now()
	h.embedHealthMu.Unlock()
	return result
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
