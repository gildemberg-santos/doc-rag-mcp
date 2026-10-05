package search

// Busca semântica com pool + rerank híbrido (vetor + keyword) + diversify.
//
// Resolve os gaps observados na análise MCP-vs-grep:
//   - top_k lotado pelo mesmo arquivo  → diversify (cap por arquivo)
//   - categorias sumindo (Vindi/Stripe) → rerank keyword (match literal pesa)
//   - query curta convergindo p/ cluster dominante → boost de filename + tf
import (
	"context"
	"sort"
	"strings"

	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/vector"
)

// Params da busca.
type Params struct {
	Query      string
	Projects   []string // vazio = todos os projetos
	TopK       int      // default 5, máx 50
	Diversify  bool     // default true: máx MaxPerFile chunks por arquivo
	MaxPerFile int      // default 2
	PathPrefix string   // ex: "app/controllers/" (filtro client-side)
	Language   string   // ex: "rb" (igual ao campo language do chunk)
	PureVector bool     // true = desliga rerank híbrido (score cosseno puro)
}

// Result é um chunk ranqueado.
type Result struct {
	Project  string
	Path     string
	Language string
	Score    float64 // final (fusão) ou cosseno puro
	Cos      float64
	Kw       float64 // keyword normalizado 0..1 no pool
	Strength string  // forte|moderado|fraco (heurística)
	Content  string
}

// ParseProjects aceita "a,b" ou "" (todos).
func ParseProjects(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (p *Params) normalize() {
	if p.TopK <= 0 {
		p.TopK = 5
	}
	if p.TopK > 50 {
		p.TopK = 50
	}
	if p.MaxPerFile <= 0 {
		p.MaxPerFile = 2
	}
	p.PathPrefix = strings.Trim(strings.TrimSpace(p.PathPrefix), "/")
}

// Run executa: embed → pool vetorial → filtros → rerank → diversify → topK.
func Run(ctx context.Context, q *vector.Client, e embed.Provider, p Params) ([]Result, error) {
	p.normalize()
	if strings.TrimSpace(p.Query) == "" {
		return nil, errEmptyQuery{}
	}
	vec, err := e.EmbedQuery(ctx, p.Query)
	if err != nil {
		return nil, err
	}

	// Pool maior que o topK para dar margem ao rerank/diversify/filtros.
	poolLimit := p.TopK * 4
	if poolLimit < 20 {
		poolLimit = 20
	}
	if p.PathPrefix != "" || p.Language != "" {
		poolLimit = 200
	}
	if poolLimit > 200 {
		poolLimit = 200
	}

	// Busca por projeto (Qdrant filtra 1 projeto por chamada) e junta.
	projects := p.Projects
	if len(projects) == 0 {
		projects = []string{""}
	}
	var pool []vector.ScoredPoint
	for _, proj := range projects {
		hits, err := q.Search(ctx, vec, proj, poolLimit)
		if err != nil {
			return nil, err
		}
		pool = append(pool, hits...)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].Score > pool[j].Score })

	// Filtros client-side.
	toks := tokenize(p.Query)
	results := make([]Result, 0, len(pool))
	for _, h := range pool {
		proj, _ := h.Payload["project"].(string)
		path, _ := h.Payload["path"].(string)
		lang, _ := h.Payload["language"].(string)
		content, _ := h.Payload["content"].(string)
		if p.PathPrefix != "" && !strings.HasPrefix(path, p.PathPrefix) {
			continue
		}
		if p.Language != "" && !strings.EqualFold(lang, p.Language) {
			continue
		}
		kw := keywordScore(toks, path, content)
		results = append(results, Result{
			Project: proj, Path: path, Language: lang,
			Cos: h.Score, Kw: kw, Content: content,
		})
	}

	// Normaliza keyword no pool e funde (α=0.7 vetor, β=0.3 keyword).
	fuse(results, p.PureVector)
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	// Diversify: máx N chunks por (projeto, arquivo), ordenado pelo melhor score.
	if p.Diversify {
		results = applyDiversify(results, p.MaxPerFile)
	}

	if len(results) > p.TopK {
		results = results[:p.TopK]
	}
	for i := range results {
		results[i].Strength = strength(results[i].Score)
	}
	return results, nil
}

// fuse combina cosseno + keyword normalizada no pool.
func fuse(results []Result, pureVector bool) {
	if pureVector {
		for i := range results {
			results[i].Score = results[i].Cos
		}
		return
	}
	maxKw := 0.0
	for _, r := range results {
		if r.Kw > maxKw {
			maxKw = r.Kw
		}
	}
	if maxKw == 0 {
		for i := range results {
			results[i].Score = results[i].Cos
		}
		return
	}
	for i := range results {
		results[i].Kw /= maxKw
		results[i].Score = 0.7*results[i].Cos + 0.3*results[i].Kw
	}
}

// applyDiversify limita a maxPerFile chunks por (projeto, arquivo),
// preservando a ordem de score.
func applyDiversify(results []Result, maxPerFile int) []Result {
	seen := map[string]int{}
	div := results[:0]
	for _, r := range results {
		k := r.Project + "\x00" + r.Path
		if seen[k] >= maxPerFile {
			continue
		}
		seen[k]++
		div = append(div, r)
	}
	return div
}

// strength calibra o cosseno bruto em bandas legíveis.
// Heurística empírica em embeddings de código (OpenAI small):
// bons matches caem em 0.45–0.60; abaixo de 0.35 quase sempre é ruído.
func strength(s float64) string {
	switch {
	case s >= 0.55:
		return "forte"
	case s >= 0.40:
		return "moderado"
	default:
		return "fraco"
	}
}

var stopwords = map[string]bool{
	"de": true, "da": true, "do": true, "das": true, "dos": true,
	"em": true, "um": true, "uma": true, "os": true, "as": true,
	"o": true, "a": true, "e": true, "que": true, "com": true,
	"the": true, "of": true, "to": true, "in": true, "on": true,
	"for": true, "and": true, "como": true, "para": true,
}

// tokenize minúsculo, só [a-z0-9_], len>=3, sem stopwords curtas.
func tokenize(q string) []string {
	q = strings.ToLower(q)
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	})
	var out []string
	for _, t := range fields {
		if len(t) < 3 || stopwords[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// keywordScore soma ocorrências dos tokens no conteúdo (peso 1),
// no path (peso 2) e boost se o token está no nome do arquivo (+3).
func keywordScore(toks []string, path, content string) float64 {
	if len(toks) == 0 {
		return 0
	}
	lc := strings.ToLower(content)
	lp := strings.ToLower(path)
	base := lp
	if i := strings.LastIndex(lp, "/"); i >= 0 {
		base = lp[i+1:]
	}
	score := 0.0
	for _, t := range toks {
		score += float64(strings.Count(lc, t))
		score += 2 * float64(strings.Count(lp, t))
		if strings.Contains(base, t) {
			score += 3
		}
	}
	return score
}

type errEmptyQuery struct{}

func (errEmptyQuery) Error() string { return "query é obrigatória" }
