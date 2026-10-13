package indexer

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
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

// scrollPageSize é a paginação do diff incremental — 1000 pontos por página
// (eram 500) para reduzir o nº de roundtrips no atendimento (~38k pontos).
const scrollPageSize = 1000

// maxEmbedAttempts é o nº de tentativas (1 inicial + retries) de cada lote
// de embed/upsert antes de desistir — com backoff de 500ms * tentativa.
const maxEmbedAttempts = 3

// Options controla o comportamento do Run.
type Options struct {
	// Clean=false (default) é a indexação incremental: só reembeda chunks
	// novos/alterados (por hash) e remove pontos obsoletos — nunca apaga
	// tudo antes. Clean=true é o reindex completo (apaga o projeto antes),
	// reservado para migrações de dims/modelo. --watch sempre força Clean=false.
	Clean bool
	// EmbedBatchSize é o tamanho do lote de embed+upsert processado de uma
	// vez (streaming — memória fica limitada a esse tamanho, não ao
	// projeto inteiro). <=0 usa o default (64).
	EmbedBatchSize int
	// EmbedConcurrency é quantos lotes de embed+upsert rodam em paralelo
	// (o scan continua acumulando o próximo lote enquanto os anteriores
	// embedam). <=0 usa o default (4). Use 1 para comportamento
	// estritamente sequencial (ex.: testes determinísticos).
	EmbedConcurrency int
	// DryRun simula o ciclo sem mutar nada: não chama o provider de
	// embedding, não faz upsert, não deleta (nem DeleteByProject nem
	// pontos obsoletos) e não recria coleção — só conta e loga o que
	// seria feito (inalterados, a embedar, obsoletos).
	DryRun bool
	// OnProgress é chamado a cada lote persistido com (chunksEmbedados,
	// chunksEscaneadosAtéAgora). Pode ser nil. Pode ser chamado de
	// múltiplas goroutines — implementações devem ser thread-safe.
	OnProgress func(embedded, scanned int)
}

// Run indexa um projeto: scan -> embed -> upsert no Qdrant.
// Usa o embedder local (Ollama); dims vêm de e.Dims().
// Loga o tempo de cada etapa com gatinhos. 🐱
func Run(ctx context.Context, projectName, projectDir string, q *vector.Client, e embed.Provider, clean bool) (int, error) {
	return RunWithOptions(ctx, projectName, projectDir, q, e, Options{Clean: clean})
}

// withRetry executa fn até maxEmbedAttempts vezes, com backoff de
// 500ms * tentativa entre elas. Desiste de imediato se o contexto foi
// cancelado — retry em cima de Ctrl-C/SIGTERM só atrasaria o desligamento.
func withRetry(ctx context.Context, what string, fn func() error) error {
	var err error
	for attempt := 1; attempt <= maxEmbedAttempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if attempt < maxEmbedAttempts {
			log.Printf("🐾 retry %s (tentativa %d/%d): %v", what, attempt, maxEmbedAttempts, err)
			select {
			case <-ctx.Done():
				return err
			case <-time.After(time.Duration(attempt*500) * time.Millisecond):
			}
		}
	}
	return err
}

// RunWithOptions é o Run com progresso e timing detalhado.
func RunWithOptions(ctx context.Context, projectName, projectDir string, q *vector.Client, e embed.Provider, opts Options) (int, error) {
	t0 := time.Now()
	mode := "reindex 🔄"
	if opts.DryRun {
		mode = "dry-run 🧪"
	} else if !opts.Clean {
		mode = "incremental ➕"
	}
	log.Printf("🐾 [%s] começando (%s, provider=%s)...", projectName, mode, e.Name())

	tColl := time.Now()
	if err := q.EnsureCollection(ctx, e.Dims()); err != nil {
		return 0, fmt.Errorf("ensure collection: %w", err)
	}
	// Índices payload `project` + `path`: sem eles, todo scroll filtrado
	// (list_docs, get_document, diff incremental) é full-scan no Qdrant.
	// Best-effort — nunca falha a indexação por isso.
	if err := q.EnsurePayloadIndex(ctx); err != nil {
		log.Printf("🐾 [%s] aviso: não criou índice payload project: %v", projectName, err)
	}
	if err := q.EnsureKeywordIndex(ctx, "path"); err != nil {
		log.Printf("🐾 [%s] aviso: não criou índice payload path: %v", projectName, err)
	}
	// Guarda de dims (item 6): trocar de modelo/dims sem recriar a coleção
	// mistura vetores incompatíveis. Incremental aborta com instrução clara;
	// --clean recria a coleção (único caminho que resolve, já que
	// DeleteByProject não muda o tamanho fixo do vetor).
	if info, err := q.CollectionInfo(ctx); err != nil {
		log.Printf("🐾 [%s] aviso: não conferi dims da coleção: %v", projectName, err)
	} else if info.VectorSize != 0 && info.VectorSize != e.Dims() {
		if opts.DryRun {
			log.Printf("🐾 [%s] dry-run: dims da coleção (%d) != provider (%d) — com --clean a coleção seria recriada",
				projectName, info.VectorSize, e.Dims())
			if !opts.Clean {
				return 0, fmt.Errorf("dims mismatch: coleção %q tem %d dims, provider %s espera %d — rode com --clean para recriar (apaga e reindexa tudo)",
					q.Collection, info.VectorSize, e.Name(), e.Dims())
			}
		} else if opts.Clean {
			log.Printf("🐾 [%s] dims da coleção (%d) != provider (%d) — recriando a coleção...",
				projectName, info.VectorSize, e.Dims())
			if err := q.DeleteCollection(ctx); err != nil {
				return 0, fmt.Errorf("recriar coleção (delete): %w", err)
			}
			if err := q.EnsureCollection(ctx, e.Dims()); err != nil {
				return 0, fmt.Errorf("recriar coleção (ensure): %w", err)
			}
			if err := q.EnsurePayloadIndex(ctx); err != nil {
				log.Printf("🐾 [%s] aviso: não recriou índice payload project: %v", projectName, err)
			}
		} else {
			return 0, fmt.Errorf("dims mismatch: coleção %q tem %d dims, provider %s espera %d — rode com --clean para recriar (apaga e reindexa tudo)",
				q.Collection, info.VectorSize, e.Name(), e.Dims())
		}
	}
	if opts.Clean && !opts.DryRun {
		tDel := time.Now()
		if err := q.DeleteByProject(ctx, projectName); err != nil {
			log.Printf("🐾 [%s] aviso: não apagou pontos antigos: %v", projectName, err)
		} else {
			log.Printf("🐾 [%s] pontos antigos apagados em %s", projectName, time.Since(tDel).Round(time.Millisecond))
		}
	} else if opts.Clean {
		log.Printf("🐾 [%s] dry-run: pularia o delete de todos os pontos do projeto (modo --clean)", projectName)
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
	// bodyToIDs: bodyHash -> um ponto existente com aquele corpo (só pontos
	// com o esquema novo têm body_hash; os antigos simplesmente não entram
	// e caem no caminho de reembed de antes). fileHashToPath: fileHash ->
	// path existente, para o fast-path de log de rename/move.
	bodyToIDs := map[string]string{}
	fileHashToPath := map[string]string{}
	if !opts.Clean {
		tDiff := time.Now()
		filter := map[string]any{
			"must": []any{
				map[string]any{"key": "project", "match": map[string]any{"value": projectName}},
			},
		}
		var offset any
		for {
			pts, next, err := q.ScrollFields(ctx, filter, scrollPageSize, offset, []string{"hash", "path", "mtime", "size", "chunk_count", "body_hash", "file_hash"})
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
				if bh, _ := p.Payload["body_hash"].(string); bh != "" {
					if _, ok := bodyToIDs[bh]; !ok {
						bodyToIDs[bh] = id
					}
				}

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
				if fh, _ := p.Payload["file_hash"].(string); fh != "" {
					if _, ok := fileHashToPath[fh]; !ok {
						fileHashToPath[fh] = path
					}
				}
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
	concurrency := opts.EmbedConcurrency
	if concurrency <= 0 {
		concurrency = 4
	}

	tScan := time.Now()
	indexedAt := time.Now().UTC().Format(time.RFC3339)
	newByID := map[string]bool{}
	var pendingChunks []chunker.Chunk
	var pendingIDs []string
	// copyPending: chunks cujo corpo já existe no índice (rename/move,
	// deslocamento, duplicata) — aguardam cópia de vetor, sem embed.
	type copyTask struct {
		chunk chunker.Chunk
		id    string
		oldID string
	}
	var copyPending []copyTask
	// scannedFiles: path -> fileHash visto no scan atual (fast-path de rename).
	scannedFiles := map[string]string{}
	// Contadores atômicos: o scan roda na goroutine principal enquanto os
	// lotes de embed/upsert rodam em workers — tudo aqui é acessado dos
	// dois lados. skippedFast = pulado sem ler (mtime+size+count);
	// hashHits = lido mas com hash igual (economizou embed, não I/O);
	// copied = vetor copiado de ponto existente (economizou embed).
	var scanned, skippedFast, hashHits, embeddedTotal, copied atomic.Int64
	var mu sync.Mutex // só para firstErr
	var firstErr error
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	failed := func() error {
		mu.Lock()
		defer mu.Unlock()
		return firstErr
	}

	// buildPoint monta o ponto Qdrant com payload fresco (metadados atuais do
	// scan) sobre um vetor qualquer — vindo do provider (embed novo) ou de
	// um ponto existente (cópia por reuso de conteúdo).
	buildPoint := func(c chunker.Chunk, id string, vec []float32) vector.Point {
		return vector.Point{
			ID:     id,
			Vector: vec,
			Payload: map[string]any{
				"project":     c.Project,
				"path":        c.Path,
				"language":    c.Language,
				"chunk_index": c.ChunkIndex,
				"content":     c.Content,
				"hash":        c.Hash,
				"body_hash":   c.BodyHash,
				"file_hash":   c.FileHash,
				"indexed_at":  indexedAt,
				"mtime":       c.ModTime.UTC().Format(time.RFC3339Nano),
				"size":        c.Size,
				"chunk_count": c.ChunkCount,
			},
		}
	}

	// embedAndUpsert processa UM lote com retry (3 tentativas, backoff
	// 500ms*tentativa) tanto no Embed quanto no Upsert.
	embedAndUpsert := func(chunks []chunker.Chunk, ids []string) error {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}
		tEmbed := time.Now()
		var vecs [][]float32
		if err := withRetry(ctx, "embed", func() error {
			var err error
			vecs, err = e.Embed(ctx, texts)
			return err
		}); err != nil {
			return err
		}
		points := make([]vector.Point, len(chunks))
		for i, c := range chunks {
			points[i] = buildPoint(c, ids[i], vecs[i])
		}
		if err := withRetry(ctx, "upsert", func() error {
			return q.Upsert(ctx, points)
		}); err != nil {
			return err
		}
		n := embeddedTotal.Add(int64(len(points)))
		log.Printf("🐱 [%s] lote: +%d chunks embedados/upsertados em %s (total até agora: %d)",
			projectName, len(points), time.Since(tEmbed).Round(time.Millisecond), n)
		if opts.OnProgress != nil {
			opts.OnProgress(int(n), int(scanned.Load()))
		}
		return nil
	}

	// submit envia um lote para os workers (com backpressure: bloqueia se o
	// pool estiver cheio, então o scan pausa em vez de acumular memória).
	// No DryRun só conta, sem chamar provider nem Qdrant.
	submit := func(chunks []chunker.Chunk, ids []string) error {
		if err := failed(); err != nil {
			return err
		}
		if len(chunks) == 0 {
			return nil
		}
		if opts.DryRun {
			n := embeddedTotal.Add(int64(len(chunks)))
			if opts.OnProgress != nil {
				opts.OnProgress(int(n), int(scanned.Load()))
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := embedAndUpsert(chunks, ids); err != nil {
				fail(err)
			}
		}()
		return nil
	}

	flush := func() error {
		if len(pendingChunks) == 0 {
			return nil
		}
		chunks, ids := pendingChunks, pendingIDs
		pendingChunks = nil
		pendingIDs = nil
		return submit(chunks, ids)
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
		skippedFast.Add(int64(len(m.ids)))
		scanned.Add(int64(len(m.ids)))
		return true
	}

	onChunk := func(c chunker.Chunk) error {
		scanned.Add(1)
		id := pointID(c.Project, c.Path, c.ChunkIndex)
		newByID[id] = true
		if _, ok := scannedFiles[c.Path]; !ok {
			scannedFiles[c.Path] = c.FileHash
		}
		if h, ok := existingHash[id]; ok && h == c.Hash {
			hashHits.Add(1)
			return nil
		}
		// Reuso por conteúdo (itens 3+7): corpo idêntico a um ponto já
		// indexado (rename/move, conteúdo deslocado ou duplicado) — o vetor
		// é copiado na fase de cópia, sem chamar o provider. O ponto antigo
		// sai na limpeza de obsoletos se de fato sumiu, ou permanece se
		// ainda é produzido (duplicata viva em outro path/índice).
		if c.BodyHash != "" {
			if oldID, ok := bodyToIDs[c.BodyHash]; ok && oldID != id {
				copyPending = append(copyPending, copyTask{chunk: c, id: id, oldID: oldID})
				return nil
			}
		}
		pendingChunks = append(pendingChunks, c)
		pendingIDs = append(pendingIDs, id)
		if len(pendingChunks) >= embedBatchSize {
			return flush()
		}
		return nil
	}

	// scanErr coleta o erro do scan e do flush final; o wg.Wait() abaixo é
	// obrigatório em todos os caminhos (inclusive erro): sem ele, um lote
	// com falha abortaria o scan e a função retornaria com workers ainda em
	// voo (goroutines vazadas + upserts após o retorno).
	scanErr := chunker.ScanProject(projectName, projectDir, nil, shouldSkip, onChunk)
	if scanErr == nil {
		scanErr = flush()
	}
	wg.Wait()
	if ferr := failed(); ferr != nil {
		return 0, ferr
	}
	if scanErr != nil {
		return 0, scanErr
	}
	if scanned.Load() == 0 {
		return 0, fmt.Errorf("nenhum arquivo indexável em %s", projectDir)
	}

	// Fast-path de log de rename/move: path novo cujo fileHash bate com um
	// path que sumiu do scan = move, não conteúdo novo (os vetores vêm pela
	// fase de cópia abaixo). Só log, sem efeito no processamento.
	if !opts.Clean && len(scannedFiles) > 0 && len(fileHashToPath) > 0 {
		moves := 0
		for path, fh := range scannedFiles {
			if fh == "" {
				continue
			}
			if _, known := existingMeta[path]; known {
				continue
			}
			oldPath, ok := fileHashToPath[fh]
			if !ok || oldPath == path {
				continue
			}
			if _, stillThere := scannedFiles[oldPath]; stillThere {
				continue
			}
			moves++
			if moves <= 10 {
				log.Printf("🐾 [%s] movido: %s → %s (vetores copiados, sem reembed)", projectName, oldPath, path)
			}
		}
		if moves > 0 {
			log.Printf("🐾 [%s] %d arquivo(s) reaproveitados por move/rename", projectName, moves)
		}
	}

	// Fase de cópia: um Retrieve em lote busca os vetores-fonte e um único
	// Upsert grava os novos pontos — zero chamadas ao provider de embedding.
	// Fontes que sumiram entre o scroll e o retrieve (condição de corrida
	// raríssima) caem no fallback de reembed síncrono.
	if len(copyPending) > 0 {
		if opts.DryRun {
			copied.Add(int64(len(copyPending)))
		} else {
			want := map[string]bool{}
			for _, t := range copyPending {
				want[t.oldID] = true
			}
			oldIDs := make([]string, 0, len(want))
			for id := range want {
				oldIDs = append(oldIDs, id)
			}
			vecByID := map[string][]float32{}
			if vecs, err := q.RetrieveVectors(ctx, oldIDs); err != nil {
				log.Printf("🐾 [%s] aviso: não recuperei %d vetores para reuso, reembedando: %v", projectName, len(oldIDs), err)
			} else {
				for _, v := range vecs {
					vecByID[v.ID] = v.Vector
				}
			}
			var copyPoints []vector.Point
			var fallback []copyTask
			for _, t := range copyPending {
				if vec, ok := vecByID[t.oldID]; ok {
					copyPoints = append(copyPoints, buildPoint(t.chunk, t.id, vec))
				} else {
					fallback = append(fallback, t)
				}
			}
			if len(copyPoints) > 0 {
				tCopy := time.Now()
				if err := withRetry(ctx, "upsert-cópias", func() error {
					return q.Upsert(ctx, copyPoints)
				}); err != nil {
					return 0, err
				}
				copied.Add(int64(len(copyPoints)))
				log.Printf("🐱 [%s] reuso: +%d chunks com vetor copiado (sem reembed) em %s",
					projectName, len(copyPoints), time.Since(tCopy).Round(time.Millisecond))
				if opts.OnProgress != nil {
					opts.OnProgress(int(embeddedTotal.Load()), int(scanned.Load()))
				}
			}
			for _, t := range fallback {
				if err := embedAndUpsert([]chunker.Chunk{t.chunk}, []string{t.id}); err != nil {
					return 0, err
				}
			}
		}
	}

	unchanged := skippedFast.Load() + hashHits.Load()
	embedded := embeddedTotal.Load()
	reused := copied.Load()
	if opts.DryRun {
		log.Printf("🐾 [%s] scan (dry-run): %d chunks (%d pulados sem ler, %d com hash igual, %d copiados, %d a embedar) em %s",
			projectName, scanned.Load(), skippedFast.Load(), hashHits.Load(), reused, embedded, time.Since(tScan).Round(time.Millisecond))
	} else {
		log.Printf("🐾 [%s] scan+embed: %d chunks (%d pulados sem ler, %d com hash igual, %d copiados, %d embedados) em %s",
			projectName, scanned.Load(), skippedFast.Load(), hashHits.Load(), reused, embedded, time.Since(tScan).Round(time.Millisecond))
	}

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
		if opts.DryRun {
			log.Printf("🐾 [%s] dry-run: %d pontos obsoletos seriam removidos", projectName, len(stale))
		} else if err := func() error {
			tDelStale := time.Now()
			if err := q.DeletePoints(ctx, stale); err != nil {
				return err
			}
			log.Printf("🐾 [%s] %d pontos obsoletos removidos em %s", projectName, len(stale), time.Since(tDelStale).Round(time.Millisecond))
			return nil
		}(); err != nil {
			log.Printf("🐾 [%s] aviso: não removeu %d pontos obsoletos: %v", projectName, len(stale), err)
		}
	}

	total := int(unchanged + embedded + reused)
	verb := "indexados"
	if opts.DryRun {
		verb = "contados (dry-run, nada foi escrito)"
	}
	log.Printf("✅ [%s] %d chunks %s (%d pulados sem ler, %d com hash igual, %d copiados, %d novos/alterados, %d removidos) — total em %s 🐱",
		projectName, total, verb, skippedFast.Load(), hashHits.Load(), reused, embedded, len(stale), time.Since(t0).Round(time.Millisecond))
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
