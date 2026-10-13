package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"doc-rag-mcp/internal/embed"
	"doc-rag-mcp/internal/vector"
)

func TestTokenize(t *testing.T) {
	got := tokenize("Como funciona o webhook de pagamento?")
	want := map[string]bool{"como": false, "funciona": true, "webhook": true, "pagamento": true}
	// "como" é stopword; "o"/"de" curtos fora
	for _, tok := range got {
		if tok == "como" || tok == "o" || tok == "de" {
			t.Fatalf("token inesperado: %q (tokens=%v)", tok, got)
		}
	}
	for w := range want {
		if !want[w] {
			continue
		}
		found := false
		for _, tok := range got {
			if tok == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("token %q ausente em %v", w, got)
		}
	}
	if len(tokenize("webhook")) != 1 {
		t.Fatal("query de 1 termo deveria gerar 1 token")
	}
}

func TestKeywordScoreFilenameBoost(t *testing.T) {
	toks := []string{"webhook"}
	withName := keywordScore(toks, "app/services/vtex/process_webhook_service.rb", "algum conteúdo neutro xyz")
	without := keywordScore(toks, "app/models/user.rb", "algum conteúdo neutro xyz")
	if withName <= without {
		t.Fatalf("filename boost falhou: com=%v sem=%v", withName, without)
	}
}

func TestFuseOrdersKeywordMatchFirst(t *testing.T) {
	// Simula o caso da análise: arquivo denso repetido vs arquivo com match literal.
	res := []Result{
		{Project: "p", Path: "b.rb", Cos: 0.52, Kw: 1},  // 1 ocorrência
		{Project: "p", Path: "a.rb", Cos: 0.50, Kw: 12}, // muitas ocorrências
		{Project: "p", Path: "c.rb", Cos: 0.49, Kw: 6},
	}
	fuse(res, false)
	if !(res[0].Score > 0 && res[1].Score > res[0].Score) {
		t.Fatalf("fusão não reordenou pelo keyword: %+v", res)
	}
	// maxKw=12 → Kw normalizados: b=1/12, a=1.0
	if res[1].Kw != 1.0 {
		t.Fatalf("Kw deveria normalizar o maior para 1.0: %+v", res)
	}
}

func TestFusePureVectorKeepsCosine(t *testing.T) {
	res := []Result{{Cos: 0.5, Kw: 10}, {Cos: 0.4, Kw: 100}}
	fuse(res, true)
	if res[0].Score != 0.5 || res[1].Score != 0.4 {
		t.Fatalf("pure_vector deveria manter cosseno: %+v", res)
	}
}

func TestApplyDiversify(t *testing.T) {
	// 3 chunks do mesmo arquivo no topo + 2 de outros.
	res := []Result{
		{Project: "p", Path: "hot.rb", Cos: 0.9},
		{Project: "p", Path: "hot.rb", Cos: 0.89},
		{Project: "p", Path: "hot.rb", Cos: 0.88},
		{Project: "p", Path: "cold_a.rb", Cos: 0.5},
		{Project: "p", Path: "cold_b.rb", Cos: 0.49},
	}
	for i := range res {
		res[i].Score = res[i].Cos
	}
	div := applyDiversify(res, 1)
	if len(div) != 3 {
		t.Fatalf("esperava 3 (1 por arquivo), veio %d", len(div))
	}
	if div[0].Path != "hot.rb" || div[1].Path != "cold_a.rb" || div[2].Path != "cold_b.rb" {
		t.Fatalf("ordem errada: %+v", div)
	}
}

func TestStrengthBands(t *testing.T) {
	if strength(0.6) != "forte" || strength(0.5) != "moderado" || strength(0.3) != "fraco" {
		t.Fatal("bandas de score erradas")
	}
}

func TestParseProjects(t *testing.T) {
	if len(ParseProjects("")) != 0 {
		t.Fatal("vazio deveria dar nenhum projeto")
	}
	got := ParseProjects("neurolead, neurolead-client")
	if len(got) != 2 || got[0] != "neurolead" || got[1] != "neurolead-client" {
		t.Fatalf("parse csv errado: %v", got)
	}
}

func TestSnippet(t *testing.T) {
	s := snippet("linha um\n  match webhook aqui \nlinha três", "webhook")
	if !strings.Contains(s, "webhook") {
		t.Fatalf("snippet deveria conter o match: %q", s)
	}
}

func TestParamsNormalizeCaps(t *testing.T) {
	p := Params{TopK: 500, MaxPerFile: 99}
	p.normalize()
	if p.TopK != 50 {
		t.Errorf("top_k deveria capar em 50, veio %d", p.TopK)
	}
	p = Params{}
	p.normalize()
	if p.TopK != 5 || p.MaxPerFile != 2 {
		t.Errorf("defaults errados: %+v", p)
	}
	p = Params{PathPrefix: "/app/controllers/"}
	p.normalize()
	if p.PathPrefix != "app/controllers" {
		t.Errorf("prefix deveria trimar barras: %q", p.PathPrefix)
	}
}

func TestStrengthBoundaries(t *testing.T) {
	if strength(0.55) != "forte" || strength(0.549) != "moderado" {
		t.Error("fronteira 0.55 errada")
	}
	if strength(0.40) != "moderado" || strength(0.399) != "fraco" {
		t.Error("fronteira 0.40 errada")
	}
}

func TestFuseZeroKeywordFallsBackToCosine(t *testing.T) {
	res := []Result{{Cos: 0.5, Kw: 0}, {Cos: 0.4, Kw: 0}}
	fuse(res, false)
	if res[0].Score != 0.5 || res[1].Score != 0.4 {
		t.Fatalf("sem keyword, score deveria ser o cosseno: %+v", res)
	}
}

func TestApplyDiversifyRespectsMaxPerFile(t *testing.T) {
	mk := func(path string, score float64) Result {
		return Result{Project: "p", Path: path, Score: score}
	}
	res := []Result{mk("a.rb", 0.9), mk("a.rb", 0.8), mk("a.rb", 0.7), mk("b.rb", 0.6)}
	div := applyDiversify(res, 2)
	if len(div) != 3 {
		t.Fatalf("max_per_file=2 deveria dar 3, veio %d", len(div))
	}
}

func TestRunEndToEndWithFakes(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.5, 0.5}}})
	}))
	defer embedSrv.Close()
	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "1", "score": 0.9, "payload": map[string]any{
				"project": "p", "path": "app/a.rb", "language": "rb", "content": "webhook aqui",
			}},
			map[string]any{"id": "2", "score": 0.8, "payload": map[string]any{
				"project": "p", "path": "spec/b.rb", "language": "rb", "content": "nada relevante",
			}},
		}}})
	}))
	defer qdrantSrv.Close()

	e := embed.NewOllama(embedSrv.URL, "m", 2)
	q := vector.New(qdrantSrv.URL, "c")

	res, err := Run(context.Background(), q, e, Params{Query: "webhook", PathPrefix: "app/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Path != "app/a.rb" {
		t.Fatalf("prefix deveria filtrar só app/a.rb: %+v", res)
	}
	if res[0].Strength == "" {
		t.Error("resultado deveria ter banda de força")
	}

	if _, err := Run(context.Background(), q, e, Params{Query: "  "}); err == nil {
		t.Error("query vazia deveria ser erro")
	}
}
