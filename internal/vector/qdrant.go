package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client é um cliente REST minimalista do Qdrant (sem gRPC).
// Usa a API /collections + /points/query (Qdrant >= 1.7).
type Client struct {
	BaseURL    string
	Collection string
	HTTP       *http.Client
	// APIKey, quando não-vazia, vai no header `api-key` (Qdrant com
	// QDRANT_API_KEY configurado recusa tudo sem ela — ver WithAPIKey).
	APIKey string
}

func New(baseURL, collection string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Collection: collection,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
}

// WithAPIKey deriva um cliente autenticado (mesma conexão/coleção).
// Sem key, o comportamento é o atual (Qdrant aberto, rede confiável).
func (c *Client) WithAPIKey(key string) *Client {
	cp := *c
	cp.APIKey = key
	return &cp
}

func (c *Client) do(ctx context.Context, method, path string, payload any, out any) (int, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("api-key", c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if out != nil && len(respBytes) > 0 {
		_ = json.Unmarshal(respBytes, out)
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("qdrant %s %s -> %d: %s", method, path, resp.StatusCode, string(respBytes))
	}
	return resp.StatusCode, nil
}

// Tuning do otimizador aplicado a toda coleção criada por este projeto —
// mais agressivo que o default do Qdrant (deleted_threshold 0.2,
// vacuum_min_vector_number 1000), pra que vetores deletados (soft-delete
// da reindexação incremental) sejam aspirados com pouco acúmulo em vez de
// milhares parados. Validado ao vivo: 2690 → 155 pendentes.
// (Coleções que já existiam antes disso precisam do PATCH manual único
// documentado no README — por decisão, EnsureCollection não altera
// coleção existente pra não desfazer tuning proposital de ninguém.)
const (
	OptimizerDeletedThreshold = 0.1
	OptimizerVacuumMinVectors = 500
)

// EnsureCollection cria a coleção se não existir — e, nesse caso, já
// aplica o tuning do otimizador num PATCH separado (o PUT de criação não
// aceita a config parcial com a mesma tolerância do PATCH, verificado
// contra o Qdrant real).
func (c *Client) EnsureCollection(ctx context.Context, vectorSize int) error {
	var existing map[string]any
	status, err := c.do(ctx, "GET", "/collections/"+c.Collection, nil, &existing)
	if err == nil && status == 200 {
		return nil // já existe — não toca em tuning alheio
	}
	payload := map[string]any{
		"vectors": map[string]any{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}
	if _, err := c.do(ctx, "PUT", "/collections/"+c.Collection, payload, nil); err != nil {
		return err
	}
	_, err = c.do(ctx, "PATCH", "/collections/"+c.Collection, map[string]any{
		"optimizers_config": map[string]any{
			"deleted_threshold":        OptimizerDeletedThreshold,
			"vacuum_min_vector_number": OptimizerVacuumMinVectors,
		},
	}, nil)
	return err
}

// DeleteCollection apaga a coleção inteira (usado só na migração de
// dims/modelo com --clean: vetores com dims diferentes são incompatíveis e
// DeleteByProject não resolve, porque o tamanho do vetor é fixo na criação).
func (c *Client) DeleteCollection(ctx context.Context) error {
	_, err := c.do(ctx, "DELETE", "/collections/"+c.Collection, nil, nil)
	return err
}

// EnsurePayloadIndex cria o índice payload `project` (keyword) se ainda não
// existir — sem ele, todo Scroll com filtro por projeto vira full-scan no
// Qdrant, o que dói a cada ciclo incremental em coleções grandes (~38k
// pontos no atendimento). Best-effort: erro aqui só gera aviso no chamador,
// nunca falha a indexação (o filtro continua funcionando, só mais lento).
func (c *Client) EnsurePayloadIndex(ctx context.Context) error {
	return c.EnsureKeywordIndex(ctx, "project")
}

// EnsureKeywordIndex cria um índice payload keyword sobre o campo indicado
// (ex.: "project", "path"). Best-effort — nunca deve falhar o chamador.
func (c *Client) EnsureKeywordIndex(ctx context.Context, field string) error {
	_, err := c.do(ctx, "PUT", "/collections/"+c.Collection+"/index", map[string]any{
		"field_name":   field,
		"field_schema": "keyword",
	}, nil)
	return err
}

// WithCollection deriva um cliente para outra coleção no mesmo Qdrant
// (ex.: a coleção "-filedocs" de documentação gerada) — reaproveita
// BaseURL, HTTP client e APIKey já configurados.
func (c *Client) WithCollection(collection string) *Client {
	cp := *c
	cp.Collection = collection
	return &cp
}

// Point representa um chunk indexado.
type Point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// Upsert insere/atualiza pontos em lote.
func (c *Client) Upsert(ctx context.Context, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	// Qdrant aceita lotes grandes, mas fatiamos em 128 por segurança.
	const batch = 128
	for i := 0; i < len(points); i += batch {
		end := i + batch
		if end > len(points) {
			end = len(points)
		}
		payload := map[string]any{"points": points[i:end]}
		if _, err := c.do(ctx, "PUT", "/collections/"+c.Collection+"/points", payload, nil); err != nil {
			return err
		}
	}
	return nil
}

// ScoredPoint é o resultado de busca.
type ScoredPoint struct {
	ID      any            `json:"id"`
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

// Search faz busca vetorial com filtro opcional por projeto.
func (c *Client) Search(ctx context.Context, queryVec []float32, project string, limit int) ([]ScoredPoint, error) {
	if limit <= 0 {
		limit = 5
	}
	payload := map[string]any{
		"query":        queryVec,
		"limit":        limit,
		"with_payload": true,
	}
	if project != "" {
		payload["filter"] = map[string]any{
			"must": []any{
				map[string]any{"key": "project", "match": map[string]any{"value": project}},
			},
		}
	}
	var out struct {
		Result struct {
			Points []ScoredPoint `json:"points"`
		} `json:"result"`
	}
	if _, err := c.do(ctx, "POST", "/collections/"+c.Collection+"/points/query", payload, &out); err != nil {
		return nil, err
	}
	return out.Result.Points, nil
}

// DeleteByProject remove todos os pontos de um projeto (para reindex).
func (c *Client) DeleteByProject(ctx context.Context, project string) error {
	payload := map[string]any{
		"filter": map[string]any{
			"must": []any{
				map[string]any{"key": "project", "match": map[string]any{"value": project}},
			},
		},
	}
	_, err := c.do(ctx, "POST", "/collections/"+c.Collection+"/points/delete", payload, nil)
	return err
}

// DeletePoints remove pontos por ID explícito (usado para limpar chunks
// obsoletos — arquivo removido/renomeado/encurtado — na reindexação incremental).
func (c *Client) DeletePoints(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	const batch = 128
	for i := 0; i < len(ids); i += batch {
		end := i + batch
		if end > len(ids) {
			end = len(ids)
		}
		payload := map[string]any{"points": ids[i:end]}
		if _, err := c.do(ctx, "POST", "/collections/"+c.Collection+"/points/delete", payload, nil); err != nil {
			return err
		}
	}
	return nil
}

// RetrievedPoint é um ponto buscado por ID com seu vetor (sem score).
type RetrievedPoint struct {
	ID     string
	Vector []float32
}

// RetrieveVectors busca vetores por ID explícito, em lotes — usado pelo
// reuso por conteúdo (rename/move, conteúdo deslocado/duplicado): copiar o
// vetor já indexado sai de graça, reembedar custa tempo/dinheiro. Payload
// não é trazido (o chamador reconstrói o payload fresco com metadados
// atuais); IDs inexistentes são simplesmente omitidos da resposta.
func (c *Client) RetrieveVectors(ctx context.Context, ids []string) ([]RetrievedPoint, error) {
	var all []RetrievedPoint
	const batch = 500
	for i := 0; i < len(ids); i += batch {
		end := i + batch
		if end > len(ids) {
			end = len(ids)
		}
		filter := map[string]any{
			"must": []any{
				map[string]any{"has_id": ids[i:end]},
			},
		}
		var offset any
		for {
			payload := map[string]any{
				"limit":        len(ids[i:end]),
				"with_payload": false,
				"with_vector":  true,
				"filter":       filter,
			}
			if offset != nil {
				payload["offset"] = offset
			}
			var out struct {
				Result struct {
					Points []struct {
						ID     any       `json:"id"`
						Vector []float32 `json:"vector"`
					} `json:"points"`
					Next any `json:"next_page_offset"`
				} `json:"result"`
			}
			if _, err := c.do(ctx, "POST", "/collections/"+c.Collection+"/points/scroll", payload, &out); err != nil {
				return nil, err
			}
			for _, p := range out.Result.Points {
				id, _ := p.ID.(string)
				if id == "" || len(p.Vector) == 0 {
					continue
				}
				all = append(all, RetrievedPoint{ID: id, Vector: p.Vector})
			}
			if out.Result.Next == nil {
				break
			}
			offset = out.Result.Next
		}
	}
	return all, nil
}

// Scroll lista pontos com paginação (usado por list_projects / get_document).
func (c *Client) Scroll(ctx context.Context, filter map[string]any, limit int, offset any) ([]ScoredPoint, any, error) {
	return c.scroll(ctx, filter, limit, offset, true)
}

// ScrollFields é como Scroll, mas pede só os campos de payload indicados
// (evita trazer "content" inteiro quando só precisamos, por ex., do "hash").
func (c *Client) ScrollFields(ctx context.Context, filter map[string]any, limit int, offset any, fields []string) ([]ScoredPoint, any, error) {
	return c.scroll(ctx, filter, limit, offset, fields)
}

func (c *Client) scroll(ctx context.Context, filter map[string]any, limit int, offset any, withPayload any) ([]ScoredPoint, any, error) {
	payload := map[string]any{
		"limit":        limit,
		"with_payload": withPayload,
		"with_vector":  false,
	}
	if filter != nil {
		payload["filter"] = filter
	}
	if offset != nil {
		payload["offset"] = offset
	}
	var out struct {
		Result struct {
			Points []ScoredPoint `json:"points"`
			Next   any           `json:"next_page_offset"`
		} `json:"result"`
	}
	if _, err := c.do(ctx, "POST", "/collections/"+c.Collection+"/points/scroll", payload, &out); err != nil {
		return nil, nil, err
	}
	return out.Result.Points, out.Result.Next, nil
}

// Count retorna o nº de pontos da coleção.
func (c *Client) Count(ctx context.Context) (int64, error) {
	var out struct {
		Result struct {
			Count int64 `json:"count"`
		} `json:"result"`
	}
	if _, err := c.do(ctx, "POST", "/collections/"+c.Collection+"/points/count", map[string]any{}, &out); err != nil {
		return 0, err
	}
	return out.Result.Count, nil
}

// CountFor conta pontos de uma coleção arbitrária (não necessariamente
// c.Collection) — usado pra inspecionar coleções órfãs/legadas.
func (c *Client) CountFor(ctx context.Context, collection string) (int64, error) {
	var out struct {
		Result struct {
			Count int64 `json:"count"`
		} `json:"result"`
	}
	if _, err := c.do(ctx, "POST", "/collections/"+collection+"/points/count", map[string]any{}, &out); err != nil {
		return 0, err
	}
	return out.Result.Count, nil
}

// CollectionInfo é o resumo de saúde/config de uma coleção Qdrant.
type CollectionInfo struct {
	Status              string
	OptimizerStatus     string
	PointsCount         int64
	IndexedVectorsCount int64
	SegmentsCount       int
	VectorSize          int
	Distance            string
}

// CollectionInfo busca status/config da coleção ativa (c.Collection) —
// GET /collections/{name}, o que Count() não traz (segmentos, otimizador,
// dims configurados).
func (c *Client) CollectionInfo(ctx context.Context) (CollectionInfo, error) {
	var out struct {
		Result struct {
			Status              string `json:"status"`
			OptimizerStatus     any    `json:"optimizer_status"` // "ok" ou {"error": "..."}
			PointsCount         int64  `json:"points_count"`
			IndexedVectorsCount int64  `json:"indexed_vectors_count"`
			SegmentsCount       int    `json:"segments_count"`
			Config              struct {
				Params struct {
					Vectors struct {
						Size     int    `json:"size"`
						Distance string `json:"distance"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if _, err := c.do(ctx, "GET", "/collections/"+c.Collection, nil, &out); err != nil {
		return CollectionInfo{}, err
	}
	optimizerStatus := "ok"
	if s, ok := out.Result.OptimizerStatus.(string); ok {
		optimizerStatus = s
	} else if out.Result.OptimizerStatus != nil {
		optimizerStatus = "error"
	}
	return CollectionInfo{
		Status:              out.Result.Status,
		OptimizerStatus:     optimizerStatus,
		PointsCount:         out.Result.PointsCount,
		IndexedVectorsCount: out.Result.IndexedVectorsCount,
		SegmentsCount:       out.Result.SegmentsCount,
		VectorSize:          out.Result.Config.Params.Vectors.Size,
		Distance:            out.Result.Config.Params.Vectors.Distance,
	}, nil
}

// ListCollectionNames lista todas as coleções existentes no Qdrant (não só
// a ativa) — usado pra detectar coleções órfãs/legadas.
func (c *Client) ListCollectionNames(ctx context.Context) ([]string, error) {
	var out struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if _, err := c.do(ctx, "GET", "/collections", nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, len(out.Result.Collections))
	for i, col := range out.Result.Collections {
		names[i] = col.Name
	}
	return names, nil
}
