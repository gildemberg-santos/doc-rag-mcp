package indexer

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"doc-rag-mcp/internal/chunker"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/vector"

	"github.com/google/uuid"
)

// pointNamespace é fixo (não gerar de novo) — a identidade de cada ponto no
// Qdrant depende dele ser estável entre execuções/builds.
var pointNamespace = uuid.MustParse("6f1d9a0e-2b7c-4e4a-9c3a-7a6f8f1d2e3b")

// pointID deriva um UUID determinístico para um chunk (mesmo projeto+path+
// índice → sempre o mesmo ID), permitindo que upserts sobrescrevam em vez
// de duplicar pontos entre reindexações incrementais.
func pointID(project, path string, chunkIndex int) string {
	return uuid.NewSHA1(pointNamespace, []byte(project+"|"+path+"|"+strconv.Itoa(chunkIndex))).String()
}

// Options controla o comportamento do Run.
type Options struct {
	// Clean=true (default) apaga os pontos antigos do projeto antes:
	// é a "reindexação". Clean=false faz indexação incremental.
	Clean bool
	// OnProgress é chamado a cada lote de embeddings com
	// (chunksProntos, chunksTotal). Pode ser nil.
	OnProgress func(done, total int)
}

// Run indexa um projeto: scan -> embed -> upsert no Qdrant.
// Aceita qualquer embed.Provider (OpenAI ou Ollama); dims vêm de e.Dims().
// Loga o tempo de cada etapa com gatinhos. 🐱
func Run(ctx context.Context, projectName, projectDir string, q *vector.Client, e embed.Provider, clean bool) (int, error) {
	return RunWithOptions(ctx, projectName, projectDir, q, e, Options{Clean: clean})
}

// RunWithOptions é o Run com progresso e timing detalhado.
func RunWithOptions(ctx context.Context, projectName, projectDir string, q *vector.Client, e embed.Provider, opts Options) (int, error) {
	t0 := time.Now()
	mode := "reindex 🔄"
	if !opts.Clean {
		mode = "incremental ➕"
	}
	log.Printf("🐾 [%s] começando (%s, provider=%s)...", projectName, mode, e.Name())

	tColl := time.Now()
	if err := q.EnsureCollection(ctx, e.Dims()); err != nil {
		return 0, fmt.Errorf("ensure collection: %w", err)
	}
	if opts.Clean {
		tDel := time.Now()
		if err := q.DeleteByProject(ctx, projectName); err != nil {
			log.Printf("🐾 [%s] aviso: não apagou pontos antigos: %v", projectName, err)
		} else {
			log.Printf("🐾 [%s] pontos antigos apagados em %s", projectName, time.Since(tDel).Round(time.Millisecond))
		}
	}
	log.Printf("🐾 [%s] coleção pronta em %s", projectName, time.Since(tColl).Round(time.Millisecond))

	tScan := time.Now()
	chunks, err := chunker.ScanProject(projectName, projectDir, nil)
	if err != nil {
		return 0, err
	}
	if len(chunks) == 0 {
		return 0, fmt.Errorf("nenhum arquivo indexável em %s", projectDir)
	}
	log.Printf("🐾 [%s] scan: %d chunks em %s", projectName, len(chunks), time.Since(tScan).Round(time.Millisecond))

	ids := make([]string, len(chunks))
	newByID := make(map[string]bool, len(chunks))
	for i, c := range chunks {
		id := pointID(c.Project, c.Path, c.ChunkIndex)
		ids[i] = id
		newByID[id] = true
	}

	// existingHash: pontoID -> hash de conteúdo já indexado. Em modo clean
	// fica vazio (tudo foi apagado acima), então tudo abaixo é tratado
	// como novo — mesmo comportamento de hoje, sem caminho especial.
	existingHash := map[string]string{}
	if !opts.Clean {
		tDiff := time.Now()
		filter := map[string]any{
			"must": []any{
				map[string]any{"key": "project", "match": map[string]any{"value": projectName}},
			},
		}
		var offset any
		for {
			pts, next, err := q.ScrollFields(ctx, filter, 500, offset, []string{"hash"})
			if err != nil {
				return 0, fmt.Errorf("scroll pontos existentes: %w", err)
			}
			for _, p := range pts {
				id, _ := p.ID.(string)
				if id == "" {
					continue
				}
				h, _ := p.Payload["hash"].(string)
				existingHash[id] = h
			}
			if next == nil {
				break
			}
			offset = next
		}
		log.Printf("🐾 [%s] diff: %d pontos existentes em %s", projectName, len(existingHash), time.Since(tDiff).Round(time.Millisecond))
	}

	var toEmbed []chunker.Chunk
	var toEmbedIDs []string
	unchanged := 0
	for i, c := range chunks {
		if h, ok := existingHash[ids[i]]; ok && h == c.Hash {
			unchanged++
			continue
		}
		toEmbed = append(toEmbed, c)
		toEmbedIDs = append(toEmbedIDs, ids[i])
	}

	var stale []string
	for id := range existingHash {
		if !newByID[id] {
			stale = append(stale, id)
		}
	}

	if len(toEmbed) == 0 {
		log.Printf("🐾 [%s] nada para reembeder — %d chunks inalterados", projectName, unchanged)
	} else {
		texts := make([]string, len(toEmbed))
		for i, c := range toEmbed {
			texts[i] = c.Content
		}

		// Embeddings em lotes com progresso (o provider também fatia
		// internamente; aqui fatiamos para poder reportar + cronometrar).
		tEmbed := time.Now()
		const batchSize = 64
		vecs := make([][]float32, 0, len(texts))
		nextMilestone := 10
		for i := 0; i < len(texts); i += batchSize {
			end := i + batchSize
			if end > len(texts) {
				end = len(texts)
			}
			b, err := e.Embed(ctx, texts[i:end])
			if err != nil {
				return 0, err
			}
			vecs = append(vecs, b...)
			if opts.OnProgress != nil {
				opts.OnProgress(end, len(texts))
			}
			pct := end * 100 / len(texts)
			if pct >= nextMilestone || end == len(texts) {
				elapsed := time.Since(tEmbed).Round(time.Second)
				rate := float64(end) / time.Since(tEmbed).Seconds()
				log.Printf("🐱 [%s] embeddings %d/%d (%d%%) em %s — %.0f chunks/s", projectName, end, len(texts), pct, elapsed, rate)
				nextMilestone += 10
			}
		}
		log.Printf("🐱 [%s] embeddings: %d vetores em %s", projectName, len(vecs), time.Since(tEmbed).Round(time.Millisecond))

		tUp := time.Now()
		indexedAt := time.Now().UTC().Format(time.RFC3339)
		points := make([]vector.Point, len(toEmbed))
		for i, c := range toEmbed {
			points[i] = vector.Point{
				ID:     toEmbedIDs[i],
				Vector: vecs[i],
				Payload: map[string]any{
					"project":     c.Project,
					"path":        c.Path,
					"language":    c.Language,
					"chunk_index": c.ChunkIndex,
					"content":     c.Content,
					"hash":        c.Hash,
					"indexed_at":  indexedAt,
				},
			}
		}
		if err := q.Upsert(ctx, points); err != nil {
			return 0, err
		}
		log.Printf("🐱 [%s] %d chunks upsertados em %s", projectName, len(points), time.Since(tUp).Round(time.Millisecond))
	}

	// Remove pontos cujo chunk não existe mais no scan atual (arquivo
	// apagado/renomeado/encurtado). Só depois do upsert, pra não abrir uma
	// janela em que um arquivo alterado desaparece da busca.
	if len(stale) > 0 {
		tDelStale := time.Now()
		if err := q.DeletePoints(ctx, stale); err != nil {
			log.Printf("🐾 [%s] aviso: não removeu %d pontos obsoletos: %v", projectName, len(stale), err)
		} else {
			log.Printf("🐾 [%s] %d pontos obsoletos removidos em %s", projectName, len(stale), time.Since(tDelStale).Round(time.Millisecond))
		}
	}

	total := unchanged + len(toEmbed)
	log.Printf("✅ [%s] %d chunks indexados (%d inalterados, %d novos/alterados, %d removidos) — total em %s 🐱",
		projectName, total, unchanged, len(toEmbed), len(stale), time.Since(t0).Round(time.Millisecond))
	return total, nil
}

// DiscoverProjects lista subpastas de root como projetos.
// Se root contém arquivos (não só pastas), trata o próprio root como um projeto "default".
func DiscoverProjects(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var projects []string
	for _, e := range entries {
		if e.IsDir() {
			projects = append(projects, e.Name())
		}
	}
	if len(projects) == 0 {
		// root é o próprio projeto
		projects = []string{filepath.Base(root)}
	}
	return projects, nil
}

// ProjectDir resolve o diretório de um projeto.
func ProjectDir(root, project string) string {
	candidate := filepath.Join(root, project)
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return candidate
	}
	// fallback: o root inteiro é o projeto
	return root
}
