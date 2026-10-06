package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OllamaEmbedder gera embeddings 100% locais via Ollama.
// Modelo padrão: nomic-embed-text (768 dims, ~274MB).
// Requer: ollama serve + `ollama pull nomic-embed-text`.
// API: POST {base}/api/embed {model, input: []string} -> {embeddings: [[...]]}
type OllamaEmbedder struct {
	BaseURL string
	Model   string
	dims    int
	Client  *http.Client
}

func NewOllama(baseURL, model string, dims int) *OllamaEmbedder {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	if dims <= 0 {
		// nomic-embed-text = 768; mxbai-embed-large = 1024
		dims = 768
		if strings.Contains(model, "mxbai-embed-large") {
			dims = 1024
		}
	}
	return &OllamaEmbedder{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		dims:    dims,
		Client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (e *OllamaEmbedder) Name() string { return ProviderOllama }
func (e *OllamaEmbedder) Dims() int    { return e.dims }

func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	const batchSize = 32 // CPU local: lotes menores que na OpenAI
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

func (e *OllamaEmbedder) embedOnce(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama indisponível em %s (ollama serve + ollama pull %s?): %w", e.BaseURL, e.Model, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return nil, fmt.Errorf("ollama /api/embed falhou: status=%d model=%s body=%v (rode: ollama pull %s)", resp.StatusCode, e.Model, errBody, e.Model)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama retornou %d embeddings para %d textos", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}

func (e *OllamaEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	vecs, err := e.Embed(ctx, []string{q})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedding vazio")
	}
	return vecs[0], nil
}

// Ping confere que o Ollama responde e tem o modelo configurado disponível
// — sem gerar nenhum embedding, bem mais barato que um Embed de verdade.
func (e *OllamaEmbedder) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", e.BaseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama indisponível em %s: %w", e.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama /api/tags respondeu status=%d", resp.StatusCode)
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	for _, m := range out.Models {
		if strings.HasPrefix(m.Name, e.Model) {
			return nil
		}
	}
	return fmt.Errorf("modelo %q não encontrado no ollama (rode: ollama pull %s)", e.Model, e.Model)
}
