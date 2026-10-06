// Package indexerstatus define o formato do heartbeat que o indexer em
// --watch escreve após cada ciclo, e que o mcp-server lê — é o canal entre
// os dois processos/containers, sem precisar de API nova nem acesso a
// socket nenhum: só um arquivo num volume compartilhado.
package indexerstatus

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ProjectCycle é o resultado de um projeto dentro de um ciclo.
type ProjectCycle struct {
	Project    string `json:"project"`
	Chunks     int    `json:"chunks"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// Status é o heartbeat completo de um ciclo do indexer --watch.
type Status struct {
	UpdatedAt       string         `json:"updated_at"`
	IntervalSeconds int            `json:"interval_seconds"`
	CycleStartedAt  string         `json:"cycle_started_at"`
	CycleDurationMs int64          `json:"cycle_duration_ms"`
	TotalChunks     int            `json:"total_chunks"`
	OKCount         int            `json:"ok_count"`
	FailCount       int            `json:"fail_count"`
	Projects        []ProjectCycle `json:"projects"`
	NextCycleAt     string         `json:"next_cycle_at,omitempty"`
}

// Write grava o status de forma atômica (arquivo temporário + rename) —
// evita que um leitor concorrente (o mcp-server, a qualquer momento) pegue
// um JSON pela metade.
func Write(path string, s Status) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read lê e decodifica o status. Erro (arquivo ausente, JSON inválido) é
// esperado e normal quando o indexer --watch não está rodando nesse
// deployment, ou ainda não completou o primeiro ciclo — quem chama decide
// se isso é uma seção opcional ausente, não uma falha do /status inteiro.
func Read(path string) (Status, error) {
	var s Status
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

// EnsureDir cria o diretório do arquivo de status se não existir — útil
// pra não exigir que o volume monte um diretório pré-criado.
func EnsureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0755)
}
