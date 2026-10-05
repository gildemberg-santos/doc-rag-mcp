package search

// GrepDocs: inventário literal complementar à busca semântica.
// Devolve arquivos distintos com match de substring (case-insensitive),
// sem teto de top_k pequeno — para "me liste TUDO que fala de X".
// Cobre apenas arquivos indexados (respeita as exclusões do chunker).
import (
	"context"
	"sort"
	"strings"

	"doc-rag-mcp/internal/vector"
)

type GrepParams struct {
	Pattern    string
	Projects   []string // vazio = todos
	PathPrefix string
	Limit      int // arquivos distintos; default 100, máx 500
}

type GrepHit struct {
	Project       string
	Path          string
	Language      string
	ChunksMatched int
	Occurrences   int
	Snippet       string // primeira linha com match (trim, ≤300 chars)
}

func (p *GrepParams) normalize() {
	if p.Limit <= 0 {
		p.Limit = 100
	}
	if p.Limit > 500 {
		p.Limit = 500
	}
	p.PathPrefix = strings.Trim(strings.TrimSpace(p.PathPrefix), "/")
}

// Grep varre o índice via scroll e agrega por arquivo.
// Retorna truncated=true se parou no Limit antes de varrer tudo.
func Grep(ctx context.Context, q *vector.Client, p GrepParams) ([]GrepHit, bool, error) {
	p.normalize()
	pat := strings.ToLower(strings.TrimSpace(p.Pattern))
	if pat == "" {
		return nil, false, errEmptyPattern{}
	}

	projects := p.Projects
	if len(projects) == 0 {
		projects = []string{""} // "" = sem filtro
	}

	byFile := map[string]*GrepHit{}
	truncated := false
outer:
	for _, proj := range projects {
		var filter map[string]any
		if proj != "" {
			filter = map[string]any{
				"must": []any{
					map[string]any{"key": "project", "match": map[string]any{"value": proj}},
				},
			}
		}
		var offset any
		for {
			pts, next, err := q.Scroll(ctx, filter, 500, offset)
			if err != nil {
				return nil, false, err
			}
			for _, pt := range pts {
				project, _ := pt.Payload["project"].(string)
				path, _ := pt.Payload["path"].(string)
				lang, _ := pt.Payload["language"].(string)
				content, _ := pt.Payload["content"].(string)
				if p.PathPrefix != "" && !strings.HasPrefix(path, p.PathPrefix) {
					continue
				}
				occ := strings.Count(strings.ToLower(content), pat)
				if occ == 0 {
					continue
				}
				key := project + "\x00" + path
				h, ok := byFile[key]
				if !ok {
					if len(byFile) >= p.Limit {
						truncated = true
						break outer
					}
					h = &GrepHit{Project: project, Path: path, Language: lang, Snippet: snippet(content, pat)}
					byFile[key] = h
				}
				h.ChunksMatched++
				h.Occurrences += occ
			}
			if next == nil {
				break
			}
			offset = next
		}
	}

	hits := make([]GrepHit, 0, len(byFile))
	for _, h := range byFile {
		hits = append(hits, *h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Occurrences != hits[j].Occurrences {
			return hits[i].Occurrences > hits[j].Occurrences
		}
		if hits[i].Project != hits[j].Project {
			return hits[i].Project < hits[j].Project
		}
		return hits[i].Path < hits[j].Path
	})
	return hits, truncated, nil
}

// snippet extrai a primeira linha contendo o padrão.
func snippet(content, lowerPat string) string {
	for _, ln := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(ln), lowerPat) {
			ln = strings.TrimSpace(ln)
			if len(ln) > 300 {
				ln = ln[:300] + "…"
			}
			return ln
		}
	}
	return ""
}

type errEmptyPattern struct{}

func (errEmptyPattern) Error() string { return "pattern é obrigatório" }
