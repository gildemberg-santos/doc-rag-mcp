package docgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OllamaGenerator gera documentação 100% local via Ollama.
// Modelo padrão: qwen2.5-coder:7b (validado no spike — doc correta em PT).
// API: POST {base}/api/generate {model, prompt, stream:false, options} ->
// {response: "..."} (com think:false o reasoning não consome o budget).
type OllamaGenerator struct {
	BaseURL     string
	Model       string
	NumCtx      int
	NumPredict  int
	Temperature float64
	Client      *http.Client
}

func NewOllama(baseURL, model string, numCtx, numPredict int, temperature float64) *OllamaGenerator {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "qwen2.5-coder:7b"
	}
	if numCtx <= 0 {
		numCtx = DefaultNumCtx
	}
	if numPredict <= 0 {
		numPredict = DefaultNumPredict
	}
	return &OllamaGenerator{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Model:       model,
		NumCtx:      numCtx,
		NumPredict:  numPredict,
		Temperature: temperature,
		Client:      &http.Client{Timeout: 10 * time.Minute},
	}
}

func (g *OllamaGenerator) Name() string { return "ollama/" + g.Model }

func (g *OllamaGenerator) Generate(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  g.Model,
		"prompt": prompt,
		"stream": false,
		"think":  false,
		"options": map[string]any{
			"temperature": g.Temperature,
			"num_predict": g.NumPredict,
			"num_ctx":     g.NumCtx,
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", g.BaseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama indisponível em %s: %w", g.BaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return "", fmt.Errorf("ollama /api/generate falhou: status=%d model=%s body=%v", resp.StatusCode, g.Model, errBody)
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Response) == "" {
		return "", fmt.Errorf("ollama retornou geração vazia (model=%s)", g.Model)
	}
	return out.Response, nil
}
