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
}

func New(baseURL, collection string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Collection: collection,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
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

// EnsureCollection cria a coleção se não existir.
func (c *Client) EnsureCollection(ctx context.Context, vectorSize int) error {
	var existing map[string]any
	status, err := c.do(ctx, "GET", "/collections/"+c.Collection, nil, &existing)
	if err == nil && status == 200 {
		return nil // já existe
	}
	payload := map[string]any{
		"vectors": map[string]any{
			"size":     vectorSize,
			"distance": "Cosine",
		},
	}
	_, err = c.do(ctx, "PUT", "/collections/"+c.Collection, payload, nil)
	return err
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
