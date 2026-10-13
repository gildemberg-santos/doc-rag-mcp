// Package filedocs persiste a documentação gerada por arquivo (internal/docgen)
// numa coleção Qdrant separada (<base>-filedocs), um ponto por arquivo —
// sem misturar com os chunks brutos do índice principal.
//
// Payload por ponto (conforme docs/plano-auto-documentacao.md §3):
//
//	project, path, language, doc_content, source_hash, model, generated_at
//
// O vetor do ponto é o embedding do próprio doc_content (prosa, não código),
// o que permite busca semântica sobre a documentação gerada. A sincronia é
// incremental por source_hash (sha256 do conteúdo do arquivo): só gera
// completion para arquivos novos/alterados; arquivos removidos têm o ponto
// apagado no mesmo ciclo.
package filedocs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"doc-rag-mcp/internal/chunker"
	"doc-rag-mcp/internal/docgen"
	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/vector"

	"github.com/google/uuid"
)

// pointNamespace é próprio (não colide com os IDs de chunks do indexer,
// que usam outro namespace + chunk_index no derivado).
var pointNamespace = uuid.MustParse("9c4e2a1f-3d5b-4e6a-8c7d-1a2b3c4d5e6f")

// PointID deriva um UUID determinístico por arquivo (mesmo projeto+path →
// sempre o mesmo ID), permitindo upserts idempotentes entre ciclos.
func PointID(project, path string) string {
	return uuid.NewSHA1(pointNamespace, []byte(project+"|"+path)).String()
}

// CollectionFor resolve a coleção de filedocs a partir da coleção base de
// chunks: "<base>-filedocs" (ex.: "docs" → "docs-filedocs",
// "docs-ollama" → "docs-ollama-filedocs"). Evita sufixo duplo.
func CollectionFor(base string) string {
	if base == "" {
		base = "docs"
	}
	if strings.HasSuffix(base, "-filedocs") {
		return base
	}
	return base + "-filedocs"
}

// SourceHash é o fingerprint do conteúdo inteiro do arquivo (hex sha256) —
// a granularidade do diff incremental do docgen. Arquivo com o mesmo
// source_hash já salvo pula a completion (sem custo de LLM).
func SourceHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum[:])
}

// Options controla o Run.
type Options struct {
	// Clean=true apaga todos os pontos do projeto antes (regeração total).
	// Default false: incremental por source_hash.
	Clean bool
	// DryRun simula sem mutar nada: não chama o LLM, não embbeda, não faz
	// upsert nem deleta — só conta e loga o que seria feito.
	DryRun bool
	// Store=false desliga a persistência no Qdrant (só gera markdown/stdout).
	// Default true. Útil para pré-visualizar sem tocar no índice.
	Store bool
	// OutDir, quando não-vazio, espelha <projeto>/<path>.md em disco para
	// cada doc gerada neste ciclo. Vazio = sem markdown (ou stdout, ver
	// PrintStdout).
	OutDir string
	// PrintStdout imprime cada doc gerada no stdout (formato markdown com
	// header). O cmd/docgen usa para o modo sem --out.
	PrintStdout bool
	// MaxBytes pula arquivos maiores (não cabem no contexto local).
	MaxBytes int
	// MinLines pula arquivos com menos linhas (código já autoexplicativo).
	MinLines int
	// Concurrency limita as generations em paralelo (completions são
	// pesadas; default 2).
	Concurrency int
	// Model é o nome do modelo de geração (vai no payload de cada ponto).
	Model string
	// OnProgress, se não-nil, é chamado ao final com (gerados, total).
	// Pode ser chamado de múltiplas goroutines — implementações devem ser
	// thread-safe (aqui é chamado uma vez só, ao final).
	OnProgress func(generated, total int)
}

// Stats resume um ciclo por projeto.
type Stats struct {
	Project   string
	Files     int // candidatos avaliados
	Generated int // completions executadas
	Skipped   int // source_hash igual (sem custo de LLM)
	Stored    int // pontos com upsert
	Removed   int // pontos de arquivos removidos apagados
}

// defaults preenche zeros com os valores da v1.
func (o *Options) defaults() {
	if o.MaxBytes <= 0 {
		o.MaxBytes = docgen.DefaultMaxBytes
	}
	if o.MinLines <= 0 {
		o.MinLines = 20
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	// Store defaults to true: só desliga explicitamente (modo só-markdown
	// do docgen standalone). Como bool zero-value é false, "nenhum destino"
	// (sem OutDir, sem PrintStdout, Store=false) significa "só persistir" —
	// é o caso do indexer --generate-docs, que nunca passa OutDir.
}

// Candidate é um arquivo elegível à documentação.
type Candidate struct {
	Rel     string
	Abs     string
	Content string
}

// ListCandidates lista arquivos candidatos: indexáveis pelo chunker (mesmas
// exclusões do embedding — segredos, lockfiles, binários), texto, com ao
// menos minLines linhas e até maxBytes bytes. Ordenados por path (saída
// determinística).
func ListCandidates(projectDir string, maxBytes, minLines int) ([]Candidate, error) {
	if maxBytes <= 0 {
		maxBytes = docgen.DefaultMaxBytes
	}
	if minLines <= 0 {
		minLines = 20
	}
	refs, err := chunker.ListFiles(projectDir, maxBytes)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range refs {
		data, err := os.ReadFile(r.Abs)
		if err != nil {
			continue
		}
		if !chunker.IsText(string(data)) {
			continue
		}
		if countLines(string(data)) < minLines {
			continue
		}
		out = append(out, Candidate{Rel: r.Rel, Abs: r.Abs, Content: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// existingEntry resume o que já está salvo para um path.
type existingEntry struct {
	id   string
	hash string
}

// loadExisting varre a coleção de filedocs filtrando por projeto e devolve
// path → (id, source_hash). Usado pelo diff incremental e pela limpeza de
// pontos de arquivos removidos.
func loadExisting(ctx context.Context, q *vector.Client, project string) (map[string]existingEntry, error) {
	out := map[string]existingEntry{}
	filter := map[string]any{
		"must": []any{
			map[string]any{"key": "project", "match": map[string]any{"value": project}},
		},
	}
	var offset any
	for {
		pts, next, err := q.ScrollFields(ctx, filter, 500, offset, []string{"path", "source_hash"})
		if err != nil {
			return nil, err
		}
		for _, p := range pts {
			path, _ := p.Payload["path"].(string)
			if path == "" {
				continue
			}
			id, _ := p.ID.(string)
			hash, _ := p.Payload["source_hash"].(string)
			out[path] = existingEntry{id: id, hash: hash}
		}
		if next == nil {
			break
		}
		offset = next
	}
	return out, nil
}

// Ensure prepara a coleção de filedocs (cria se ausente, com o mesmo tuning
// do otimizador dos chunks) + índices payload project/path. Best-effort nos
// índices — nunca falha o ciclo por isso.
func Ensure(ctx context.Context, q *vector.Client, dims int) error {
	if err := q.EnsureCollection(ctx, dims); err != nil {
		return err
	}
	if err := q.EnsureKeywordIndex(ctx, "project"); err != nil {
		log.Printf("🐾 [filedocs] aviso: não criou índice payload project: %v", err)
	}
	if err := q.EnsureKeywordIndex(ctx, "path"); err != nil {
		log.Printf("🐾 [filedocs] aviso: não criou índice payload path: %v", err)
	}
	return nil
}

// BuildPoint monta o ponto Qdrant de uma doc gerada. O vetor deve ser o
// embedding do próprio doc (prosa), calculado pelo chamador com o mesmo
// provider de embedding do índice principal.
func BuildPoint(project, rel, language, doc, sourceHash, model, generatedAt string, vec []float32) vector.Point {
	return vector.Point{
		ID:     PointID(project, rel),
		Vector: vec,
		Payload: map[string]any{
			"project":      project,
			"path":         rel,
			"language":     language,
			"doc_content":  doc,
			"source_hash":  sourceHash,
			"model":        model,
			"generated_at": generatedAt,
		},
	}
}

// MarkdownBody formata uma doc gerada como arquivo .md (mesmo formato do
// cmd/docgen standalone) — sempre com o aviso de conteúdo de LLM.
func MarkdownBody(project, rel, model, doc string) string {
	return fmt.Sprintf(`<!-- gerado por doc-rag docgen (%s) em %s — conteúdo de LLM, pode conter erros -->
# %s

Projeto: %s | Arquivo: %s

%s
`, model, time.Now().UTC().Format(time.RFC3339), rel, project, rel, doc)
}

// generateWithRetry executa uma completion com até 3 tentativas (backoff
// 500ms*tentativa). Desiste de imediato com contexto cancelado.
func generateWithRetry(ctx context.Context, g docgen.Provider, prompt string) (string, error) {
	var err error
	var doc string
	for attempt := 1; attempt <= 3; attempt++ {
		if doc, err = g.Generate(ctx, prompt); err == nil {
			if strings.TrimSpace(doc) != "" {
				return doc, nil
			}
			err = fmt.Errorf("geração vazia")
		}
		if ctx.Err() != nil {
			return "", err
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return "", err
			case <-time.After(time.Duration(attempt*500) * time.Millisecond):
			}
		}
	}
	return "", err
}

// Run gera a documentação dos arquivos de um projeto e persiste na coleção
// de filedocs (incremental por source_hash). projectDir é o checkout local;
// q é o cliente já apontando para a coleção "-filedocs"; e embbeda o
// doc_content; g gera a prosa.
func Run(ctx context.Context, project, projectDir string, q *vector.Client, e embed.Provider, g docgen.Provider, opts Options) (Stats, error) {
	opts.defaults()
	// Destino da persistência: Store=true grava no Qdrant. Quando nenhum
	// destino foi configurado (sem OutDir, sem PrintStdout, Store=false),
	// assume persistência — é o caso do indexer --generate-docs.
	doStore := opts.Store || (opts.OutDir == "" && !opts.PrintStdout)

	var st Stats
	st.Project = project

	if doStore {
		if e == nil {
			return st, fmt.Errorf("embedder é obrigatório para persistir filedocs")
		}
		if err := Ensure(ctx, q, e.Dims()); err != nil {
			return st, fmt.Errorf("ensure filedocs collection: %w", err)
		}
		if info, err := q.CollectionInfo(ctx); err == nil {
			if info.VectorSize != 0 && info.VectorSize != e.Dims() {
				return st, fmt.Errorf("dims mismatch: coleção %q tem %d dims, provider %s espera %d — apague a coleção %q e regere",
					q.Collection, info.VectorSize, e.Name(), e.Dims(), q.Collection)
			}
		} else {
			log.Printf("🐾 [filedocs:%s] aviso: não conferi dims da coleção: %v", project, err)
		}
		if opts.Clean && !opts.DryRun {
			if err := q.DeleteByProject(ctx, project); err != nil {
				log.Printf("🐾 [filedocs:%s] aviso: não apagou docs antigas: %v", project, err)
			}
		}
	}

	cands, err := ListCandidates(projectDir, opts.MaxBytes, opts.MinLines)
	if err != nil {
		return st, err
	}
	st.Files = len(cands)
	if len(cands) == 0 {
		return st, nil
	}

	existing := map[string]existingEntry{}
	if !opts.Clean && doStore {
		if existing, err = loadExisting(ctx, q, project); err != nil {
			return st, fmt.Errorf("scroll filedocs existentes: %w", err)
		}
	}

	type todo struct {
		cand Candidate
		hash string
	}
	var todos []todo
	seen := map[string]bool{}
	for _, c := range cands {
		h := SourceHash(c.Content)
		seen[c.Rel] = true
		if !opts.Clean {
			if ex, ok := existing[c.Rel]; ok && ex.hash == h {
				st.Skipped++
				continue
			}
		}
		todos = append(todos, todo{cand: c, hash: h})
	}

	// Limpeza de obsoletos: paths salvos que sumiram do scan atual.
	var stale []string
	for path, ex := range existing {
		if !seen[path] && ex.id != "" {
			stale = append(stale, ex.id)
		}
	}

	if opts.DryRun {
		st.Generated = len(todos) // seriam gerados
		st.Removed = len(stale)   // seriam removidos
		log.Printf("🐾 [filedocs:%s] dry-run: %d arquivos (%d com doc atual, %d a gerar, %d obsoletos a remover)",
			project, st.Files, st.Skipped, st.Generated, st.Removed)
		return st, nil
	}

	// Fase 1: generations em paralelo (semáforo — completions pesadas).
	type result struct {
		rel  string
		hash string
		doc  string
		err  error
	}
	results := make([]result, len(todos))
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	for i, t := range todos {
		if ctx.Err() != nil {
			results[i] = result{rel: t.cand.Rel, err: ctx.Err()}
			continue
		}
		select {
		case <-ctx.Done():
			results[i] = result{rel: t.cand.Rel, err: ctx.Err()}
			continue
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, t todo) {
			defer wg.Done()
			defer func() { <-sem }()
			prompt := docgen.PromptFor(project, t.cand.Rel, chunker.LanguageFor(t.cand.Rel), t.cand.Content)
			doc, err := generateWithRetry(ctx, g, prompt)
			results[i] = result{rel: t.cand.Rel, hash: t.hash, doc: strings.TrimSpace(doc), err: err}
		}(i, t)
	}
	wg.Wait()

	var failed int
	type docOut struct {
		rel  string
		hash string
		doc  string
	}
	var docs []docOut
	for _, r := range results {
		if r.err != nil {
			failed++
			log.Printf("😿 [filedocs:%s] %s: %v", project, r.rel, r.err)
			continue
		}
		docs = append(docs, docOut{rel: r.rel, hash: r.hash, doc: r.doc})
	}
	st.Generated = len(docs)
	if failed > 0 && len(docs) == 0 {
		return st, fmt.Errorf("%d doc(s) falharam e nenhuma foi gerada", failed)
	}

	// Markdown/stdout (não depende do Qdrant).
	if opts.OutDir != "" || opts.PrintStdout {
		for _, d := range docs {
			body := MarkdownBody(project, d.rel, opts.Model, d.doc)
			if opts.PrintStdout && opts.OutDir == "" {
				fmt.Printf("\n===== %s =====\n%s\n", d.rel, body)
			}
			if opts.OutDir != "" {
				dest := filepath.Join(opts.OutDir, project, d.rel+".md")
				if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
					log.Printf("😿 [filedocs:%s] %s: mkdir: %v", project, d.rel, err)
					continue
				}
				if err := os.WriteFile(dest, []byte(body), 0644); err != nil {
					log.Printf("😿 [filedocs:%s] %s: write: %v", project, d.rel, err)
				}
			}
		}
	}

	// Fase 2: embed dos doc_contents + upsert (só se for persistir).
	if doStore {
		texts := make([]string, len(docs))
		for i, d := range docs {
			texts[i] = d.doc
		}
		var vecs [][]float32
		if len(texts) > 0 {
			var err error
			vecs, err = e.Embed(ctx, texts)
			if err != nil {
				return st, fmt.Errorf("embed filedocs: %w", err)
			}
		}
		now := time.Now().UTC().Format(time.RFC3339)
		points := make([]vector.Point, 0, len(docs))
		for i, d := range docs {
			lang := chunker.LanguageFor(d.rel)
			points = append(points, BuildPoint(project, d.rel, lang, d.doc, d.hash, opts.Model, now, vecs[i]))
		}
		if len(points) > 0 {
			if err := q.Upsert(ctx, points); err != nil {
				return st, fmt.Errorf("upsert filedocs: %w", err)
			}
			st.Stored = len(points)
		}
		if len(stale) > 0 {
			if err := q.DeletePoints(ctx, stale); err != nil {
				log.Printf("🐾 [filedocs:%s] aviso: não removeu %d docs obsoletas: %v", project, len(stale), err)
			} else {
				st.Removed = len(stale)
			}
		}
	}

	if opts.OnProgress != nil {
		opts.OnProgress(st.Generated, st.Files)
	}
	log.Printf("📝 [filedocs:%s] %d arquivos (%d com doc atual, %d gerados, %d persistidos, %d removidos, %d falhas)",
		project, st.Files, st.Skipped, st.Generated, st.Stored, st.Removed, failed)
	if failed > 0 {
		return st, fmt.Errorf("%d arquivo(s) falharam (ver log)", failed)
	}
	return st, nil
}
