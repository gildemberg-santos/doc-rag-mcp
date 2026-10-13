package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/search"
	"doc-rag-mcp/internal/vector"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps são as dependências compartilhadas pelas tools.
type Deps struct {
	Qdrant   *vector.Client
	Embedder embed.Provider // Ollama local (mesmo modelo da indexação)
	Provider string         // nome do provider (p/ index_status)
	Dims     int            // dims do vetor (p/ index_status)
	// FileDocs aponta para a coleção "-filedocs" de documentação gerada
	// (nil = ainda sem docs geradas / seção omitida).
	FileDocs *vector.Client
	// Name/Version identificam o servidor (p/ index_status) — permite ao
	// agente detectar schema de tools desatualizado no client.
	Name    string
	Version string
}

// New constrói o servidor MCP com todas as tools RAG registradas.
func New(name, version string, d *Deps) *mcp.Server {
	d.Name = name
	d.Version = version
	s := mcp.NewServer(&mcp.Implementation{Name: name, Version: version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_docs",
		Description: "Busca semântica + keyword na documentação/código indexado. Ranking com diversidade por arquivo (1 arquivo não lota o top_k) e match literal com peso. Para inventário EXAUSTIVO de arquivos use grep_docs.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchDocsArgs) (*mcp.CallToolResult, any, error) {
		return handleSearchDocs(ctx, d, args)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "grep_docs",
		Description: "Inventário literal: lista TODOS os arquivos indexados contendo o termo (case-insensitive), com nº de ocorrências e trecho. Complementa search_docs quando precisa de completude, não só dos top resultados.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GrepDocsArgs) (*mcp.CallToolResult, any, error) {
		return handleGrepDocs(ctx, d, args)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_document",
		Description: "Retorna o conteúdo completo (todos os chunks) de um arquivo indexado, dado projeto + caminho. Use após search_docs para ler o arquivo inteiro.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetDocArgs) (*mcp.CallToolResult, any, error) {
		return handleGetDocument(ctx, d, args)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_projects",
		Description: "Lista os projetos indexados no RAG com contagem de chunks e data da última indexação.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return handleListProjects(ctx, d)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_docs",
		Description: "Lista arquivos indexados de um projeto (distintos por path), com filtro opcional por prefixo de diretório. Útil para mapear a estrutura documentada.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListDocsArgs) (*mcp.CallToolResult, any, error) {
		return handleListDocs(ctx, d, args)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "index_status",
		Description: "Retorna status do índice vetorial: provider, coleção, dims, total de pontos e URL do Qdrant.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return handleIndexStatus(ctx, d)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_file_doc",
		Description: "Retorna a documentação gerada por LLM de um arquivo (propósito, API pública, dependências). Conteúdo gerado, pode conter erros — os chunks brutos (get_document) continuam a fonte autoritativa.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetFileDocArgs) (*mcp.CallToolResult, any, error) {
		return handleGetFileDoc(ctx, d, args)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_file_docs",
		Description: "Busca semântica sobre as documentações geradas (prosa, não código). Melhor que search_docs para 'o que esse módulo faz'.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchFileDocsArgs) (*mcp.CallToolResult, any, error) {
		return handleSearchFileDocs(ctx, d, args)
	})
	return s
}

// ---- args ----

type SearchDocsArgs struct {
	Query      string `json:"query" jsonschema:"pergunta ou termo de busca semântica"`
	Project    string `json:"project,omitempty" jsonschema:"projeto(s) para filtrar, separados por vírgula; vazio busca em todos (cross-project)"`
	TopK       int    `json:"top_k,omitempty" jsonschema:"quantidade de trechos (padrão 5, máx 50)"`
	Diversify  *bool  `json:"diversify,omitempty" jsonschema:"true (padrão) limita a máx 2 trechos por arquivo para cobrir mais arquivos; false volta ao ranking puro por score"`
	MaxPerFile int    `json:"max_per_file,omitempty" jsonschema:"teto de trechos por arquivo quando diversify=true (padrão 2)"`
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"filtra por prefixo de diretório, ex: app/controllers/"`
	Language   string `json:"language,omitempty" jsonschema:"filtra por linguagem do chunk, ex: rb, jsx, md"`
	PureVector bool   `json:"pure_vector,omitempty" jsonschema:"true desliga o rerank keyword e usa só similaridade vetorial"`
}

type GrepDocsArgs struct {
	Pattern    string `json:"pattern" jsonschema:"termo literal a procurar (case-insensitive), ex: webhook"`
	Project    string `json:"project,omitempty" jsonschema:"projeto(s), separados por vírgula; vazio busca em todos"`
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"restringe a um diretório, ex: app/services/"`
	Limit      int    `json:"limit,omitempty" jsonschema:"máx de arquivos distintos (padrão 100, máx 500)"`
}

type GetDocArgs struct {
	Project string `json:"project" jsonschema:"nome do projeto"`
	Path    string `json:"path" jsonschema:"caminho relativo do arquivo (ex: app/models/user.rb)"`
	Offset  int    `json:"offset,omitempty" jsonschema:"chunk inicial (padrão 0; use para paginar arquivos grandes)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"máx de chunks (padrão 0 = arquivo todo, máx 500; CHANGELOG de 192KB tem 209 chunks — pagina para não estourar contexto)"`
}

type ListDocsArgs struct {
	Project string `json:"project" jsonschema:"nome do projeto"`
	Limit   int    `json:"limit,omitempty" jsonschema:"máx de arquivos (padrão 50, máx 200)"`
	Prefix  string `json:"prefix,omitempty" jsonschema:"filtra por prefixo de diretório, ex: app/services/"`
}

type GetFileDocArgs struct {
	Project string `json:"project" jsonschema:"nome do projeto"`
	Path    string `json:"path" jsonschema:"caminho relativo do arquivo (ex: app/models/user.rb)"`
}

type SearchFileDocsArgs struct {
	Query      string `json:"query" jsonschema:"pergunta ou termo de busca semântica sobre as documentações geradas"`
	Project    string `json:"project,omitempty" jsonschema:"projeto(s) para filtrar, separados por vírgula; vazio busca em todos (cross-project)"`
	TopK       int    `json:"top_k,omitempty" jsonschema:"quantidade de arquivos (padrão 5, máx 20)"`
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"filtra por prefixo de diretório, ex: app/controllers/"`
}

// ---- handlers ----

func handleSearchDocs(ctx context.Context, d *Deps, args SearchDocsArgs) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(args.Query) == "" {
		return errResult("query é obrigatória"), nil, nil
	}
	diversify := true
	if args.Diversify != nil {
		diversify = *args.Diversify
	}
	hits, err := search.Run(ctx, d.Qdrant, d.Embedder, search.Params{
		Query:      args.Query,
		Projects:   search.ParseProjects(args.Project),
		TopK:       args.TopK,
		Diversify:  diversify,
		MaxPerFile: args.MaxPerFile,
		PathPrefix: args.PathPrefix,
		Language:   args.Language,
		PureVector: args.PureVector,
	})
	if err != nil {
		return errResult(fmt.Sprintf("falha na busca: %v", err)), nil, nil
	}
	if len(hits) == 0 {
		return textResult("Nenhum trecho encontrado. Tente reformular a query, ampliar o top_k, remover filtros — ou use grep_docs para inventário literal."), nil, nil
	}
	files := map[string]bool{}
	for _, h := range hits {
		files[h.Project+"\x00"+h.Path] = true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Encontrados %d trechos em %d arquivos", len(hits), len(files))
	if args.Project != "" {
		fmt.Fprintf(&sb, " (projetos: %s)", args.Project)
	}
	sb.WriteString(" — ranking com diversidade por arquivo + match literal; relevância: forte ≥0.55, moderado ≥0.40, fraco <0.40.\n\n")
	for i, h := range hits {
		content := h.Content
		if len(content) > 1500 {
			content = content[:1500] + "\n…(truncado, use get_document para o arquivo completo)"
		}
		fmt.Fprintf(&sb, "### %d. [%s] %s [força %s · score %.3f = 0.7·vetor %.3f + 0.3·keyword %.2f]\n%s\n\n",
			i+1, h.Project, h.Path, h.Strength, h.Score, h.Cos, h.Kw, content)
	}
	return textResult(sb.String()), nil, nil
}

func handleGrepDocs(ctx context.Context, d *Deps, args GrepDocsArgs) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(args.Pattern) == "" {
		return errResult("pattern é obrigatório"), nil, nil
	}
	hits, truncated, err := search.Grep(ctx, d.Qdrant, search.GrepParams{
		Pattern:    args.Pattern,
		Projects:   search.ParseProjects(args.Project),
		PathPrefix: args.PathPrefix,
		Limit:      args.Limit,
	})
	if err != nil {
		return errResult(fmt.Sprintf("falha no grep: %v", err)), nil, nil
	}
	if len(hits) == 0 {
		return textResult(fmt.Sprintf("Nenhum arquivo indexado contém %q. Nota: cobre só arquivos indexados (locks/minificados são excluídos).", args.Pattern)), nil, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Arquivos distintos com %q: %d", args.Pattern, len(hits))
	if truncated {
		sb.WriteString(" (limite atingido — refine com project/path_prefix ou aumente limit)")
	}
	sb.WriteString("\nUse search_docs/get_document para entender cada grupo.\n\n")
	for i, h := range hits {
		fmt.Fprintf(&sb, "%d. [%s] %s [%s] — %d ocorrências em %d chunks\n   └ %s\n",
			i+1, h.Project, h.Path, h.Language, h.Occurrences, h.ChunksMatched, h.Snippet)
	}
	return textResult(sb.String()), nil, nil
}

func handleGetDocument(ctx context.Context, d *Deps, args GetDocArgs) (*mcp.CallToolResult, any, error) {
	if args.Project == "" || args.Path == "" {
		return errResult("project e path são obrigatórios"), nil, nil
	}
	filter := map[string]any{
		"must": []any{
			map[string]any{"key": "project", "match": map[string]any{"value": args.Project}},
			map[string]any{"key": "path", "match": map[string]any{"value": args.Path}},
		},
	}
	var all []map[string]any
	var offset any
	for {
		pts, next, err := d.Qdrant.Scroll(ctx, filter, 100, offset)
		if err != nil {
			return errResult(fmt.Sprintf("falha ao ler documento: %v", err)), nil, nil
		}
		for _, p := range pts {
			all = append(all, p.Payload)
		}
		if next == nil {
			break
		}
		offset = next
	}
	if len(all) == 0 {
		return textResult(fmt.Sprintf("Arquivo '%s' não encontrado no projeto '%s'. Use list_docs para ver arquivos disponíveis.", args.Path, args.Project)), nil, nil
	}
	sort.Slice(all, func(i, j int) bool {
		ai, _ := all[i]["chunk_index"].(float64)
		aj, _ := all[j]["chunk_index"].(float64)
		return ai < aj
	})
	// Paginação: offset/limit sobre chunks ordenados (default = tudo, como
	// antes). Protege o contexto do agente em arquivos grandes (ex.:
	// CHANGELOG de 192KB = 209 chunks numa resposta só).
	offsetArg := args.Offset
	if offsetArg < 0 {
		offsetArg = 0
	}
	limitArg := args.Limit
	if limitArg < 0 {
		limitArg = 0
	}
	if limitArg > 500 {
		limitArg = 500
	}
	total := len(all)
	if offsetArg >= total && (limitArg > 0 || offsetArg > 0) {
		return textResult(fmt.Sprintf("Offset %d além do total (%s tem %d chunks). Use offset entre 0 e %d.", offsetArg, args.Path, total, total-1)), nil, nil
	}
	end := total
	truncated := false
	if limitArg > 0 && offsetArg < total {
		end = offsetArg + limitArg
		if end > total {
			end = total
		} else if end < total {
			truncated = true
		}
	}
	var sb strings.Builder
	if limitArg > 0 {
		fmt.Fprintf(&sb, "# %s (projeto %s, chunks %d–%d de %d)\n\n", args.Path, args.Project, offsetArg, end, total)
	} else {
		fmt.Fprintf(&sb, "# %s (projeto %s, %d chunks)\n\n", args.Path, args.Project, total)
	}
	seen := map[string]bool{}
	if offsetArg < total {
		for _, p := range all[offsetArg:end] {
			content, _ := p["content"].(string)
			// remove o header repetido após o primeiro chunk
			if seen[content] {
				continue
			}
			seen[content] = true
			sb.WriteString(content)
			sb.WriteString("\n\n---\n\n")
		}
	}
	if truncated {
		fmt.Fprintf(&sb, "\n…(resposta parcial: use offset=%d para continuar)\n", end)
	}
	return textResult(sb.String()), nil, nil
}

func handleListProjects(ctx context.Context, d *Deps) (*mcp.CallToolResult, any, error) {
	counts := map[string]int{}
	fresh := map[string]string{} // projeto -> máx indexed_at
	var offset any
	for {
		// Só os 2 campos usados (sem "content"): scroll cheio aqui
		// transfere ~100MB no índice atual — ver ScrollFields.
		pts, next, err := d.Qdrant.ScrollFields(ctx, nil, 1000, offset, []string{"project", "indexed_at"})
		if err != nil {
			return errResult(fmt.Sprintf("falha ao listar projetos: %v", err)), nil, nil
		}
		for _, p := range pts {
			proj, _ := p.Payload["project"].(string)
			if proj == "" {
				continue
			}
			counts[proj]++
			if ts, _ := p.Payload["indexed_at"].(string); ts > fresh[proj] {
				fresh[proj] = ts
			}
		}
		if next == nil {
			break
		}
		offset = next
	}
	if len(counts) == 0 {
		return textResult("Nenhum projeto indexado ainda. Rode o indexer (make index-all ou docker compose run indexer)."), nil, nil
	}
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("Projetos indexados:\n")
	for _, n := range names {
		ts := fresh[n]
		if ts == "" {
			ts = "antes do rastreio de data — reindexe para ativar"
		}
		fmt.Fprintf(&sb, "- %s (%d chunks, atualizado em %s)\n", n, counts[n], ts)
	}
	return textResult(sb.String()), nil, nil
}

func handleListDocs(ctx context.Context, d *Deps, args ListDocsArgs) (*mcp.CallToolResult, any, error) {
	if args.Project == "" {
		return errResult("project é obrigatório"), nil, nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	prefix := strings.Trim(strings.TrimSpace(args.Prefix), "/")
	filter := map[string]any{
		"must": []any{
			map[string]any{"key": "project", "match": map[string]any{"value": args.Project}},
		},
	}
	seen := map[string]string{} // path -> language
	var offset any
	for len(seen) < limit {
		// Sem "content": só path+language trafegam (scroll cheio aqui
		// transfere dezenas de MB à toa).
		pts, next, err := d.Qdrant.ScrollFields(ctx, filter, 500, offset, []string{"path", "language"})
		if err != nil {
			return errResult(fmt.Sprintf("falha ao listar docs: %v", err)), nil, nil
		}
		for _, p := range pts {
			path, _ := p.Payload["path"].(string)
			lang, _ := p.Payload["language"].(string)
			if prefix != "" && !strings.HasPrefix(path, prefix) {
				continue
			}
			if _, ok := seen[path]; !ok {
				seen[path] = lang
			}
			if len(seen) >= limit {
				break
			}
		}
		if next == nil {
			break
		}
		offset = next
	}
	if len(seen) == 0 {
		return textResult(fmt.Sprintf("Nenhum arquivo indexado para o projeto '%s' com esse filtro.", args.Project)), nil, nil
	}
	// has_doc: marca quais arquivos já têm documentação gerada (coleção
	// -filedocs). Best-effort: sem FileDocs configurado ou com erro de
	// leitura, lista sem a marca em vez de falhar.
	hasDoc := map[string]bool{}
	if d.FileDocs != nil {
		if paths, err := fileDocPaths(ctx, d.FileDocs, args.Project); err == nil {
			for _, p := range paths {
				hasDoc[p] = true
			}
		} else {
			// best-effort — a lista de chunks continua válida sem a marca
		}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Arquivos do projeto '%s' (%d):\n", args.Project, len(paths))
	for _, p := range paths {
		if hasDoc[p] {
			fmt.Fprintf(&sb, "- %s [%s] · doc gerada\n", p, seen[p])
		} else {
			fmt.Fprintf(&sb, "- %s [%s]\n", p, seen[p])
		}
	}
	return textResult(sb.String()), nil, nil
}

// fileDocPaths lista os paths com documentação gerada num projeto
// (scroll da coleção -filedocs, só o campo path).
func fileDocPaths(ctx context.Context, q *vector.Client, project string) ([]string, error) {
	filter := map[string]any{
		"must": []any{
			map[string]any{"key": "project", "match": map[string]any{"value": project}},
		},
	}
	var out []string
	var offset any
	for {
		pts, next, err := q.ScrollFields(ctx, filter, 500, offset, []string{"path"})
		if err != nil {
			return nil, err
		}
		for _, p := range pts {
			if path, _ := p.Payload["path"].(string); path != "" {
				out = append(out, path)
			}
		}
		if next == nil {
			break
		}
		offset = next
	}
	return out, nil
}

func handleIndexStatus(ctx context.Context, d *Deps) (*mcp.CallToolResult, any, error) {
	count, err := d.Qdrant.Count(ctx)
	if err != nil {
		return errResult(fmt.Sprintf("qdrant inacessível: %v", err)), nil, nil
	}
	prov := d.Provider
	if prov == "" {
		prov = "ollama"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Qdrant OK\n- Servidor: %s v%s\n- URL: %s\n- Provider: %s (%d dims)\n- Coleção: %s\n- Pontos: %d",
		d.Name, d.Version, d.Qdrant.BaseURL, prov, d.Dims, d.Qdrant.Collection, count)
	// Cobertura de docs geradas (best-effort): quantos arquivos têm doc por
	// projeto na coleção -filedocs. Sem FileDocs configurado, seção omitida.
	if d.FileDocs != nil {
		if cov, err := countFileDocs(ctx, d.FileDocs); err == nil {
			fmt.Fprintf(&sb, "\n- Docs geradas: %d arquivos em %s", cov.total, d.FileDocs.Collection)
			for _, pc := range cov.byProject {
				fmt.Fprintf(&sb, "\n  · %s: %d", pc.project, pc.count)
			}
		}
	}
	return textResult(sb.String()), nil, nil
}

type projectCount struct {
	project string
	count   int
}

type fileDocsCoverage struct {
	total     int
	byProject []projectCount
}

// countFileDocs conta pontos da coleção -filedocs agrupados por projeto.
func countFileDocs(ctx context.Context, q *vector.Client) (fileDocsCoverage, error) {
	var cov fileDocsCoverage
	counts := map[string]int{}
	var offset any
	for {
		pts, next, err := q.ScrollFields(ctx, nil, 500, offset, []string{"project"})
		if err != nil {
			return cov, err
		}
		for _, p := range pts {
			proj, _ := p.Payload["project"].(string)
			if proj == "" {
				continue
			}
			counts[proj]++
			cov.total++
		}
		if next == nil {
			break
		}
		offset = next
	}
	for proj, n := range counts {
		cov.byProject = append(cov.byProject, projectCount{proj, n})
	}
	sort.Slice(cov.byProject, func(i, j int) bool { return cov.byProject[i].project < cov.byProject[j].project })
	return cov, nil
}

func handleGetFileDoc(ctx context.Context, d *Deps, args GetFileDocArgs) (*mcp.CallToolResult, any, error) {
	if args.Project == "" || args.Path == "" {
		return errResult("project e path são obrigatórios"), nil, nil
	}
	if d.FileDocs == nil {
		return textResult("Documentação gerada ainda não disponível neste servidor (coleção -filedocs não configurada)."), nil, nil
	}
	filter := map[string]any{
		"must": []any{
			map[string]any{"key": "project", "match": map[string]any{"value": args.Project}},
			map[string]any{"key": "path", "match": map[string]any{"value": args.Path}},
		},
	}
	pts, _, err := d.FileDocs.Scroll(ctx, filter, 5, nil)
	if err != nil {
		return errResult(fmt.Sprintf("falha ao ler doc gerada: %v", err)), nil, nil
	}
	if len(pts) == 0 {
		return textResult(fmt.Sprintf("Arquivo '%s' ainda não tem documentação gerada no projeto '%s' (rode o docgen/indexer --generate-docs).", args.Path, args.Project)), nil, nil
	}
	payload := pts[0].Payload
	doc, _ := payload["doc_content"].(string)
	model, _ := payload["model"].(string)
	generatedAt, _ := payload["generated_at"].(string)
	lang, _ := payload["language"].(string)
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s (projeto %s [%s], doc gerada por %s em %s)\n\n", args.Path, args.Project, lang, model, generatedAt)
	sb.WriteString("⚠️ Conteúdo gerado por LLM, pode conter erros — os chunks brutos (get_document) são a fonte autoritativa.\n\n")
	sb.WriteString(doc)
	return textResult(sb.String()), nil, nil
}

func handleSearchFileDocs(ctx context.Context, d *Deps, args SearchFileDocsArgs) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(args.Query) == "" {
		return errResult("query é obrigatória"), nil, nil
	}
	if d.FileDocs == nil {
		return textResult("Documentação gerada ainda não disponível neste servidor (coleção -filedocs não configurada)."), nil, nil
	}
	topK := args.TopK
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}
	vec, err := d.Embedder.EmbedQuery(ctx, args.Query)
	if err != nil {
		return errResult(fmt.Sprintf("falha na busca: %v", err)), nil, nil
	}
	projects := search.ParseProjects(args.Project)
	if len(projects) == 0 {
		projects = []string{""}
	}
	type scored struct {
		score float64
		proj  string
		path  string
		lang  string
		doc   string
		model string
	}
	var pool []scored
	prefix := strings.Trim(strings.TrimSpace(args.PathPrefix), "/")
	for _, proj := range projects {
		hits, err := d.FileDocs.Search(ctx, vec, proj, topK*4)
		if err != nil {
			return errResult(fmt.Sprintf("falha na busca: %v", err)), nil, nil
		}
		for _, h := range hits {
			path, _ := h.Payload["path"].(string)
			if prefix != "" && !strings.HasPrefix(path, prefix) {
				continue
			}
			projName, _ := h.Payload["project"].(string)
			lang, _ := h.Payload["language"].(string)
			doc, _ := h.Payload["doc_content"].(string)
			model, _ := h.Payload["model"].(string)
			pool = append(pool, scored{score: h.Score, proj: projName, path: path, lang: lang, doc: doc, model: model})
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].score > pool[j].score })
	if len(pool) > topK {
		pool = pool[:topK]
	}
	if len(pool) == 0 {
		return textResult("Nenhuma documentação gerada encontrada. Tente reformular a query — ou gere docs primeiro (docgen/indexer --generate-docs)."), nil, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Encontrados %d arquivos com doc gerada — ⚠️ conteúdo de LLM, pode conter erros.\n\n", len(pool))
	for i, h := range pool {
		content := h.doc
		if len(content) > 1500 {
			content = content[:1500] + "\n…(truncado, use get_file_doc para a doc completa)"
		}
		fmt.Fprintf(&sb, "### %d. [%s] %s [%s] · score %.3f\n%s\n\n", i+1, h.proj, h.path, h.lang, h.score, content)
	}
	return textResult(sb.String()), nil, nil
}

// ---- helpers ----

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "Erro: " + msg}},
	}
}
