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
	// EmbedBatchSize é o tamanho do lote de embed+upsert processado de uma
	// vez (streaming — memória fica limitada a esse tamanho, não ao
	// projeto inteiro). <=0 usa o default (64).
	EmbedBatchSize int
	// OnProgress é chamado a cada lote persistido com (chunksEmbedados,
	// chunksEscaneadosAtéAgora). Pode ser nil.
	OnProgress func(embedded, scanned int)
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

	// fileMeta resume, por path, o que já está indexado — usado pelo
	// shouldSkip abaixo pra decidir se vale a pena nem ler o arquivo.
	// inconsistent marca quando os pontos de um mesmo path têm mtime/size
	// divergentes entre si — sinal de que uma execução anterior foi
	// interrompida no meio da atualização daquele arquivo (parte dos
	// chunks já na versão nova, parte ainda na antiga). Nesse caso nunca
	// é seguro pular a releitura, mesmo que a contagem de chunks "bata".
	type fileMeta struct {
		mtime        string
		size         int64
		chunkCount   int
		ids          []string
		inconsistent bool
	}

	// existingHash: pontoID -> hash de conteúdo já indexado. existingMeta:
	// path -> metadados agregados dos pontos daquele path. Em modo clean
	// ambos ficam vazios (tudo foi apagado acima), então tudo abaixo é
	// tratado como novo — mesmo comportamento de hoje, sem caminho especial.
	existingHash := map[string]string{}
	existingMeta := map[string]*fileMeta{}
	if !opts.Clean {
		tDiff := time.Now()
		filter := map[string]any{
			"must": []any{
				map[string]any{"key": "project", "match": map[string]any{"value": projectName}},
			},
		}
		var offset any
		for {
			pts, next, err := q.ScrollFields(ctx, filter, 500, offset, []string{"hash", "path", "mtime", "size", "chunk_count"})
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

				path, _ := p.Payload["path"].(string)
				if path == "" {
					continue
				}
				mtime, _ := p.Payload["mtime"].(string)
				size, _ := p.Payload["size"].(float64)
				chunkCount, _ := p.Payload["chunk_count"].(float64)
				m, ok := existingMeta[path]
				if !ok {
					m = &fileMeta{mtime: mtime, size: int64(size), chunkCount: int(chunkCount)}
					existingMeta[path] = m
				} else if m.mtime != mtime || m.size != int64(size) {
					// pontos do mesmo arquivo com mtime/size diferentes entre
					// si: atualização parcial anterior. Marca e nunca pula.
					m.inconsistent = true
				}
				m.ids = append(m.ids, id)
			}
			if next == nil {
				break
			}
			offset = next
		}
		log.Printf("🐾 [%s] diff: %d pontos existentes em %s", projectName, len(existingHash), time.Since(tDiff).Round(time.Millisecond))
	}

	embedBatchSize := opts.EmbedBatchSize
	if embedBatchSize <= 0 {
		embedBatchSize = 64
	}

	tScan := time.Now()
	indexedAt := time.Now().UTC().Format(time.RFC3339)
	newByID := map[string]bool{}
	var pendingChunks []chunker.Chunk
	var pendingIDs []string
	scanned, unchanged, embeddedTotal := 0, 0, 0

	flush := func() error {
		if len(pendingChunks) == 0 {
			return nil
		}
		texts := make([]string, len(pendingChunks))
		for i, c := range pendingChunks {
			texts[i] = c.Content
		}
		tEmbed := time.Now()
		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			return err
		}
		points := make([]vector.Point, len(pendingChunks))
		for i, c := range pendingChunks {
			points[i] = vector.Point{
				ID:     pendingIDs[i],
				Vector: vecs[i],
				Payload: map[string]any{
					"project":     c.Project,
					"path":        c.Path,
					"language":    c.Language,
					"chunk_index": c.ChunkIndex,
					"content":     c.Content,
					"hash":        c.Hash,
					"indexed_at":  indexedAt,
					"mtime":       c.ModTime.UTC().Format(time.RFC3339Nano),
					"size":        c.Size,
					"chunk_count": c.ChunkCount,
				},
			}
		}
		if err := q.Upsert(ctx, points); err != nil {
			return err
		}
		embeddedTotal += len(points)
		log.Printf("🐱 [%s] lote: +%d chunks embedados/upsertados em %s (total até agora: %d)",
			projectName, len(points), time.Since(tEmbed).Round(time.Millisecond), embeddedTotal)
		if opts.OnProgress != nil {
			opts.OnProgress(embeddedTotal, scanned)
		}
		pendingChunks = pendingChunks[:0]
		pendingIDs = pendingIDs[:0]
		return nil
	}

	// shouldSkip evita até ler o arquivo quando mtime+tamanho+nº de chunks
	// batem exatamente com o que já está indexado para aquele path. As três
	// condições juntas (não só mtime) cobrem tanto o caso comum de edição
	// (muda ao menos uma delas) quanto reindexações parciais interrompidas
	// no meio (nº de pontos já salvos ≠ chunk_count esperado) — nesses
	// casos cai para o caminho normal, que relê e rehasheia o arquivo.
	shouldSkip := func(rel string, modTime time.Time, size int64) bool {
		if opts.Clean {
			return false
		}
		m, ok := existingMeta[rel]
		if !ok {
			return false
		}
		if m.inconsistent || m.mtime != modTime.UTC().Format(time.RFC3339Nano) || m.size != size || len(m.ids) != m.chunkCount {
			return false
		}
		for _, id := range m.ids {
			newByID[id] = true
		}
		unchanged += len(m.ids)
		scanned += len(m.ids)
		return true
	}

	onChunk := func(c chunker.Chunk) error {
		scanned++
		id := pointID(c.Project, c.Path, c.ChunkIndex)
		newByID[id] = true
		if h, ok := existingHash[id]; ok && h == c.Hash {
			unchanged++
			return nil
		}
		pendingChunks = append(pendingChunks, c)
		pendingIDs = append(pendingIDs, id)
		if len(pendingChunks) >= embedBatchSize {
			return flush()
		}
		return nil
	}

	if err := chunker.ScanProject(projectName, projectDir, nil, shouldSkip, onChunk); err != nil {
		return 0, err
	}
	if scanned == 0 {
		return 0, fmt.Errorf("nenhum arquivo indexável em %s", projectDir)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	log.Printf("🐾 [%s] scan+embed: %d chunks (%d inalterados) em %s", projectName, scanned, unchanged, time.Since(tScan).Round(time.Millisecond))

	// Remove pontos cujo chunk não existe mais no scan atual (arquivo
	// apagado/renomeado/encurtado). Só depois dos upserts, pra não abrir
	// uma janela em que um arquivo alterado desaparece da busca.
	var stale []string
	for id := range existingHash {
		if !newByID[id] {
			stale = append(stale, id)
		}
	}
	if len(stale) > 0 {
		tDelStale := time.Now()
		if err := q.DeletePoints(ctx, stale); err != nil {
			log.Printf("🐾 [%s] aviso: não removeu %d pontos obsoletos: %v", projectName, len(stale), err)
		} else {
			log.Printf("🐾 [%s] %d pontos obsoletos removidos em %s", projectName, len(stale), time.Since(tDelStale).Round(time.Millisecond))
		}
	}

	total := unchanged + embeddedTotal
	log.Printf("✅ [%s] %d chunks indexados (%d inalterados, %d novos/alterados, %d removidos) — total em %s 🐱",
		projectName, total, unchanged, embeddedTotal, len(stale), time.Since(t0).Round(time.Millisecond))
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
