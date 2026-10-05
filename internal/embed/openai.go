package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OpenAIEmbedder gera embeddings via API da OpenAI.
// Modelo padrão: text-embedding-3-small (1536 dims).
type OpenAIEmbedder struct {
	APIKey string
	Model  string
	dims   int
	Client *http.Client
}

func New(apiKey, model string) *OpenAIEmbedder {
	return NewWithDims(apiKey, model, 0)
}

// NewWithDims permite informar os dims (default 1536, 3072 p/ large).
func NewWithDims(apiKey, model string, dims int) *OpenAIEmbedder {
	if model == "" {
		model = "text-embedding-3-small"
	}
	if dims <= 0 {
		dims = 1536
		if model == "text-embedding-3-large" {
			dims = 3072
		}
	}
	return &OpenAIEmbedder{
		APIKey: apiKey,
		Model:  model,
		dims:   dims,
		Client: &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *OpenAIEmbedder) Name() string { return ProviderOpenAI }
func (e *OpenAIEmbedder) Dims() int    { return e.dims }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Embed embute um lote de textos (máx ~100 por chamada).
// Faz paginação interna em lotes de 64 para segurança.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.APIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurada")
	}
	const batchSize = 64
	var all [][]float32
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.embedOnce(ctx, texts[i:end])
		if err != nil {
			return nil, err
		}
		all = append(all, vecs...)
	}
	return all, nil
}

func (e *OpenAIEmbedder) embedOnce(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(embedRequest{Model: e.Model, Input: texts})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return nil, fmt.Errorf("openai embeddings falhou: status=%d body=%v", resp.StatusCode, errBody)
	}
	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(out.Data))
	for _, d := range out.Data {
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

// EmbedQuery é um atalho para um único texto.
func (e *OpenAIEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	vecs, err := e.Embed(ctx, []string{q})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedding vazio")
	}
	return vecs[0], nil
}
