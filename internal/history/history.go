// Package history guarda a série temporal do /status — "estado atual"
// virando histórico. Cada /status real (não servido do cache) anexa um
// Sample pequeno; o dashboard lê os últimos N via GET /status/history e
// desenha sparkline sem depender de Prometheus/Grafana.
//
// Dois modos:
//   - só memória (HistoryFile == ""): ring buffer, perde tudo no restart.
//     Bom pra dev/local, zero config.
//   - arquivo JSONL (HistoryFile != ""): cada sample é uma linha JSON
//     (append atômico O_APPEND). Sobrevive a restart, dá pra inspecionar
//     com `tail`, e o endpoint faz tail das últimas N linhas sem carregar
//     o arquivo inteiro. Sem rotação automática — documentado no README
//     (o arquivo cresce ~1 linha por /status real, ~17k linhas/dia no
//     pior caso com refresh de 5s; `tail -n` + cron resolve).
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Sample é um ponto da série temporal.
type Sample struct {
	Timestamp     string  `json:"t"`
	Points        int64   `json:"points"`
	RequestsTotal int64   `json:"requests_total"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	HeapAllocMB   float64 `json:"heap_alloc_mb"`
	Goroutines    int     `json:"goroutines"`
	OK            bool    `json:"ok"`
	EmbedOK       *bool   `json:"embed_ok,omitempty"`
	CollectionOK  *bool   `json:"collection_ok,omitempty"`
}

// Recorder acumula samples em memória e opcionalmente em arquivo JSONL.
type Recorder struct {
	mu       sync.Mutex
	mem      []Sample // ring: só os últimos memCap
	memCap   int
	filePath string
}

// New cria um Recorder. filePath vazio = só memória.
func New(filePath string) *Recorder {
	return &Recorder{memCap: 720, filePath: filePath} // 720 = 1h a cada 5s
}

// Append registra um sample (memória sempre; arquivo se configurado).
// Erro de arquivo é ignorado de propósito: histórico nunca pode quebrar
// o /status — é observabilidade best-effort.
func (r *Recorder) Append(s Sample) {
	if s.Timestamp == "" {
		s.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	r.mu.Lock()
	r.mem = append(r.mem, s)
	if len(r.mem) > r.memCap {
		r.mem = r.mem[len(r.mem)-r.memCap:]
	}
	path := r.filePath
	r.mu.Unlock()

	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(s)
}

// Last devolve até n samples mais recentes (memória + arquivo).
// Se há arquivo, faz tail das últimas n linhas (não carrega tudo);
// senão, devolve do buffer em memória.
func (r *Recorder) Last(n int) []Sample {
	if n <= 0 {
		n = 120
	}
	if n > 2000 {
		n = 2000
	}
	r.mu.Lock()
	path := r.filePath
	r.mu.Unlock()

	if path != "" {
		if samples, err := tailJSONL(path, n); err == nil && len(samples) > 0 {
			return samples
		}
		// arquivo ausente/vazio/corrompido: cai pro buffer em memória
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.mem) {
		n = len(r.mem)
	}
	out := make([]Sample, n)
	copy(out, r.mem[len(r.mem)-n:])
	return out
}

// tailJSONL lê as últimas n linhas válidas de um JSONL sem carregar o
// arquivo inteiro na memória de uma vez (lê tudo em scanner, mas só
// guarda as últimas n — suficiente pra arquivos de dezenas de MB; pra
// escala maior, usar rotação externa).
func tailJSONL(path string, n int) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ring := make([]Sample, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var s Sample
		if err := json.Unmarshal(line, &s); err != nil {
			continue // linha parcial (write concorrente) — pula
		}
		ring = append(ring, s)
		if len(ring) > n {
			ring = ring[len(ring)-n:]
		}
	}
	return ring, sc.Err()
}
