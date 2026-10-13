package indexer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"doc-rag-mcp/internal/vector"
)

// Testes de integração contra um Qdrant real — rodam automaticamente em
// `go test ./...` quando há um Qdrant acessível (QDRANT_URL ou
// localhost:6333), e pulam (Skip) onde não há. `make test-integration`
// força com RUN_QDRANT_INTEGRATION=1.
//
// Cobrem o que só dá pra validar com um Qdrant de verdade: diff incremental
// por hash/mtime, streaming com persistência parcial, e o "self-heal" de
// uma atualização interrompida no meio — tudo isso foi validado manualmente
// em sessões anteriores (escrevendo e apagando o teste a cada vez); aqui
// fica formalizado como teste permanente.

type fakeEmbed struct {
	mu        sync.Mutex
	calls     int
	dims      int
	failAfter int // se >0, falha a partir da (failAfter+1)-ésima chamada a Embed
}

func (f *fakeEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls++
	calls := f.calls
	failAfter := f.failAfter
	f.mu.Unlock()
	if failAfter > 0 && calls > failAfter {
		return nil, errors.New("falha simulada de embedding")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dims)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}
func (f *fakeEmbed) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	return make([]float32, f.dims), nil
}
func (f *fakeEmbed) Dims() int    { return f.dims }
func (f *fakeEmbed) Name() string { return "fake" }

// numCalls lê o contador de Embed de forma thread-safe (os workers de
// embed rodam em paralelo; leituras fora de mutex após o retorno do Run
// seriam um data race para o -race).
func (f *fakeEmbed) numCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newIntegrationQdrant devolve um client numa coleção descartável só desse
// teste (apagada no cleanup). Roda quando RUN_QDRANT_INTEGRATION=1 ou quando
// um Qdrant responde em QDRANT_URL (default localhost:6333) — ou seja, `go
// test ./...` já cobre a integração em dev local/CI com Qdrant, e pula
// (Skip, não falha) onde não há Qdrant.
func newIntegrationQdrant(t *testing.T) *vector.Client {
	t.Helper()
	url := os.Getenv("QDRANT_URL")
	if url == "" {
		url = "http://localhost:6333"
	}
	if os.Getenv("RUN_QDRANT_INTEGRATION") == "" && !qdrantReachable(url) {
		t.Skip("sem Qdrant acessível (suba com `docker compose up -d qdrant` ou defina RUN_QDRANT_INTEGRATION=1)")
	}
	collection := "indexer_integration_" + sanitize(t.Name())
	c := vector.New(url, collection)
	t.Cleanup(func() {
		req, err := http.NewRequest(http.MethodDelete, c.BaseURL+"/collections/"+c.Collection, nil)
		if err != nil {
			return
		}
		resp, err := c.HTTP.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	})
	return c
}

func sanitize(name string) string {
	out := []byte(name)
	for i, b := range out {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9') {
			out[i] = '_'
		}
	}
	return string(out)
}

// qdrantReachable sonda /collections com timeout curto — decide se a
// integração roda ou pula, sem exigir a env flag.
func qdrantReachable(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimRight(url, "/") + "/collections")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIntegrationIncrementalLifecycle(t *testing.T) {
	q := newIntegrationQdrant(t)
	ctx := context.Background()
	dir := t.TempDir()
	e := &fakeEmbed{dims: 8}
	project := "lifecycle-proj"

	writeFile(t, dir, "a.md", "# A\nconteudo inicial do arquivo A\n")
	writeFile(t, dir, "b.md", "# B\nconteudo do arquivo B\n")

	// 1. clean run: tudo novo.
	n1, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: true})
	if err != nil {
		t.Fatalf("run1 (clean): %v", err)
	}
	if n1 == 0 || e.calls == 0 {
		t.Fatalf("run1: esperava chunks indexados e chamadas de embed, veio n=%d calls=%d", n1, e.calls)
	}
	callsAfterRun1 := e.calls

	// 2. incremental, nada mudou: nenhuma chamada de embed nova (hash+mtime skip).
	n2, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: false})
	if err != nil {
		t.Fatalf("run2 (incremental, nada mudou): %v", err)
	}
	if n2 != n1 {
		t.Fatalf("run2: total deveria continuar %d, veio %d", n1, n2)
	}
	if e.calls != callsAfterRun1 {
		t.Fatalf("run2: esperava 0 chamadas novas de embed, houve %d", e.calls-callsAfterRun1)
	}

	// 3. edita a.md: só o chunk alterado deve reembedar.
	time.Sleep(10 * time.Millisecond) // garante mtime diferente em filesystems de baixa resolução
	writeFile(t, dir, "a.md", "# A\nconteudo ALTERADO de verdade agora\n")
	callsBeforeRun3 := e.calls
	n3, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: false})
	if err != nil {
		t.Fatalf("run3 (a.md alterado): %v", err)
	}
	if n3 != n2 {
		t.Fatalf("run3: total não deveria mudar só por editar conteúdo, esperado %d veio %d", n2, n3)
	}
	if e.calls == callsBeforeRun3 {
		t.Fatalf("run3: esperava reembed do chunk alterado, nenhuma chamada nova ocorreu")
	}

	// 4. remove b.md: pontos daquele arquivo devem ser limpos (total cai).
	if err := os.Remove(filepath.Join(dir, "b.md")); err != nil {
		t.Fatal(err)
	}
	n4, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: false})
	if err != nil {
		t.Fatalf("run4 (b.md removido): %v", err)
	}
	if n4 >= n3 {
		t.Fatalf("run4: total deveria cair após remover b.md, antes=%d agora=%d", n3, n4)
	}
	cnt, err := q.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cnt != int64(n4) {
		t.Fatalf("contagem no Qdrant (%d) deveria bater com o total reportado (%d)", cnt, n4)
	}
}

// fetchPoints busca no Qdrant os pontos de um (projeto, path), já com
// paginação — usado pra inspecionar o que realmente foi persistido, não só
// contagens.
func fetchPoints(t *testing.T, ctx context.Context, q *vector.Client, project, path string) []vector.ScoredPoint {
	t.Helper()
	filter := map[string]any{
		"must": []any{
			map[string]any{"key": "project", "match": map[string]any{"value": project}},
			map[string]any{"key": "path", "match": map[string]any{"value": path}},
		},
	}
	var all []vector.ScoredPoint
	var offset any
	for {
		pts, next, err := q.Scroll(ctx, filter, 100, offset)
		if err != nil {
			t.Fatalf("fetchPoints: %v", err)
		}
		all = append(all, pts...)
		if next == nil {
			break
		}
		offset = next
	}
	return all
}

// TestIntegrationEditedFileIsPersistedCorrectly edita um arquivo e valida,
// lendo de volta o que está salvo no Qdrant (não só contagens): o conteúdo
// armazenado é o do arquivo editado (não ficou com a versão antiga), o
// hash gravado bate com sha256 do conteúdo gravado, mtime/size acompanham
// a edição, e o ponto é atualizado NO MESMO ID — sem duplicar.
func TestIntegrationEditedFileIsPersistedCorrectly(t *testing.T) {
	q := newIntegrationQdrant(t)
	ctx := context.Background()
	dir := t.TempDir()
	e := &fakeEmbed{dims: 8}
	project := "content-correctness-proj"
	path := "doc.md"

	writeFile(t, dir, path, "# Doc\nversão original do conteúdo\n")
	if _, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: true}); err != nil {
		t.Fatalf("run inicial: %v", err)
	}

	before := fetchPoints(t, ctx, q, project, path)
	if len(before) != 1 {
		t.Fatalf("esperava 1 ponto pro arquivo antes da edição, veio %d", len(before))
	}
	assertPayloadIsHealthy(t, before[0].Payload)
	beforeContent, _ := before[0].Payload["content"].(string)
	beforeMTime, _ := before[0].Payload["mtime"].(string)
	beforeID := fmt.Sprint(before[0].ID)
	if !strings.Contains(beforeContent, "versão original do conteúdo") {
		t.Fatalf("conteúdo persistido não contém o texto original: %q", beforeContent)
	}

	// edita o arquivo com conteúdo bem diferente (tamanho e texto).
	time.Sleep(10 * time.Millisecond)
	newText := "# Doc\nconteúdo COMPLETAMENTE DIFERENTE depois da edição, bem maior que antes\n"
	writeFile(t, dir, path, newText)
	if _, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: false}); err != nil {
		t.Fatalf("run incremental após edição: %v", err)
	}

	after := fetchPoints(t, ctx, q, project, path)
	if len(after) != 1 {
		t.Fatalf("esperava continuar com 1 ponto só (upsert no mesmo ID, sem duplicar), veio %d", len(after))
	}
	assertPayloadIsHealthy(t, after[0].Payload)

	afterContent, _ := after[0].Payload["content"].(string)
	afterMTime, _ := after[0].Payload["mtime"].(string)
	afterID := fmt.Sprint(after[0].ID)

	if afterID != beforeID {
		t.Fatalf("ID do ponto mudou após edição (esperava upsert no mesmo ID): antes=%s depois=%s", beforeID, afterID)
	}
	if !strings.Contains(afterContent, "COMPLETAMENTE DIFERENTE") {
		t.Fatalf("conteúdo persistido não reflete a edição: %q", afterContent)
	}
	if strings.Contains(afterContent, "versão original") {
		t.Fatalf("conteúdo antigo ainda presente após a edição — não atualizou de verdade: %q", afterContent)
	}
	if afterMTime == beforeMTime {
		t.Fatalf("mtime persistido não mudou após editar o arquivo (ficou %q)", afterMTime)
	}

	cnt, err := q.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("coleção deveria ter só 1 ponto no total (nada duplicado), tem %d", cnt)
	}
}

// assertPayloadIsHealthy confere que um ponto salvo no Qdrant é
// internamente consistente: o hash gravado é realmente sha256 do conteúdo
// gravado (não ficou de uma versão antiga nem foi calculado errado), e os
// campos obrigatórios do payload estão presentes e com o tipo esperado.
func assertPayloadIsHealthy(t *testing.T, payload map[string]any) {
	t.Helper()
	content, ok := payload["content"].(string)
	if !ok || content == "" {
		t.Fatalf("payload sem 'content' válido: %+v", payload)
	}
	hash, ok := payload["hash"].(string)
	if !ok || hash == "" {
		t.Fatalf("payload sem 'hash' válido: %+v", payload)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	if hash != want {
		t.Fatalf("hash gravado não bate com sha256(content): gravado=%s esperado=%s", hash, want)
	}
	// body_hash/file_hash alimentam o reuso por conteúdo (itens 3+7) e o
	// futuro source_hash do docgen — todo ponto novo precisa tê-los.
	for _, field := range []string{"body_hash", "file_hash"} {
		if v, ok := payload[field].(string); !ok || v == "" {
			t.Errorf("payload sem campo string válido %q: %+v", field, payload)
		}
	}
	for _, field := range []string{"project", "path", "language", "mtime", "indexed_at"} {
		if v, ok := payload[field].(string); !ok || v == "" {
			t.Errorf("payload sem campo string válido %q: %+v", field, payload)
		}
	}
	if _, ok := payload["chunk_index"].(float64); !ok {
		t.Errorf("payload sem 'chunk_index' numérico: %+v", payload)
	}
	if _, ok := payload["size"].(float64); !ok {
		t.Errorf("payload sem 'size' numérico: %+v", payload)
	}
}

func TestIntegrationPartialUpdateSelfHeals(t *testing.T) {
	q := newIntegrationQdrant(t)
	ctx := context.Background()
	dir := t.TempDir()
	project := "partial-proj"

	half := func(tag string) string {
		s := ""
		for i := 0; i < 40; i++ {
			s += "linha " + tag + " de conteudo bem distinto repetida aqui \n"
		}
		return s
	}
	file := writeFile(t, dir, "two.txt", half("um")+half("dois"))

	e := &fakeEmbed{dims: 8}
	n1, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: true})
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if n1 < 2 {
		t.Fatalf("setup inválido: esperava >=2 chunks, veio %d", n1)
	}

	// Edita o arquivo mantendo o nº de chunks parecido, e simula uma falha
	// logo depois do 1º lote (batch=1): o chunk 0 fica atualizado, o
	// restante fica com a versão antiga — uma atualização parcial.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(file, []byte(half("UM-EDITADO")+half("dois")), 0644); err != nil {
		t.Fatal(err)
	}
	eFail := &fakeEmbed{dims: 8, failAfter: 1}
	// Concorrência 1: a injeção de falha é ordenada (falha no 2º lote) —
	// com workers em paralelo a ordem dos lotes não seria determinística.
	_, err = RunWithOptions(ctx, project, dir, q, eFail, Options{Clean: false, EmbedBatchSize: 1, EmbedConcurrency: 1})
	if err == nil {
		t.Fatal("esperava erro propagado da falha simulada no 2º lote")
	}

	// Próxima execução normal: o arquivo tem que ser RELIDO por completo
	// (detecção de inconsistência entre pontos do mesmo path), não pulado
	// por mtime — senão os chunks da versão antiga ficam presos pra sempre.
	eOK := &fakeEmbed{dims: 8}
	if _, err := RunWithOptions(ctx, project, dir, q, eOK, Options{Clean: false}); err != nil {
		t.Fatalf("run de recuperação: %v", err)
	}
	if eOK.calls == 0 {
		t.Fatal("esperava reembed na recuperação (atualização parcial anterior) — shouldSkip pulou indevidamente")
	}
}

func TestIntegrationStreamingPersistsPartialProgressOnFailure(t *testing.T) {
	q := newIntegrationQdrant(t)
	ctx := context.Background()
	dir := t.TempDir()
	project := "streaming-proj"

	for i := 0; i < 5; i++ {
		writeFile(t, dir, string(rune('a'+i))+".md", "# arquivo\nconteudo distinto "+string(rune('a'+i))+"\n")
	}

	e := &fakeEmbed{dims: 4, failAfter: 2}
	// Concorrência 1: a falha determinística no 3º lote garante
	// persistência parcial (lotes 1-2 salvos) — com paralelismo a ordem
	// de execução dos lotes não seria determinística.
	_, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: true, EmbedBatchSize: 1, EmbedConcurrency: 1})
	if err == nil {
		t.Fatal("esperava erro propagado da falha simulada")
	}

	cnt, err := q.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cnt == 0 {
		t.Fatal("esperava persistência parcial (lotes antes da falha já salvos), contagem veio 0")
	}
	if cnt >= 5 {
		t.Fatalf("esperava persistência PARCIAL (menos que os 5 arquivos), veio %d", cnt)
	}
}

func TestIntegrationRenameCopiesVectorsWithoutReembed(t *testing.T) {
	q := newIntegrationQdrant(t)
	ctx := context.Background()
	dir := t.TempDir()
	e := &fakeEmbed{dims: 8}
	project := "rename-proj"

	writeFile(t, dir, "a.md", "# A\nconteudo do arquivo A para renomear\n")
	n1, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: true})
	if err != nil {
		t.Fatalf("run1 (clean): %v", err)
	}
	if n1 != 1 {
		t.Fatalf("setup inválido: esperava 1 chunk, veio %d", n1)
	}
	callsAfterRun1 := e.numCalls()

	// rename preserva o conteúdo (header muda pelo path → hash cheio muda,
	// mas o corpo é idêntico → reuso por cópia de vetor, sem reembed).
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "a.md"), filepath.Join(dir, "sub", "b.md")); err != nil {
		t.Fatal(err)
	}
	n2, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: false})
	if err != nil {
		t.Fatalf("run2 (rename): %v", err)
	}
	if n2 != n1 {
		t.Fatalf("total deveria continuar %d após rename, veio %d", n1, n2)
	}
	if got := e.numCalls(); got != callsAfterRun1 {
		t.Fatalf("rename não deveria reembedar nada (cópia de vetor), %d chamadas novas de embed", got-callsAfterRun1)
	}
	if old := fetchPoints(t, ctx, q, project, "a.md"); len(old) != 0 {
		t.Fatalf("pontos do path antigo deveriam ter sido removidos, restaram %d", len(old))
	}
	moved := fetchPoints(t, ctx, q, project, "sub/b.md")
	if len(moved) != 1 {
		t.Fatalf("esperava 1 ponto no path novo, veio %d", len(moved))
	}
	assertPayloadIsHealthy(t, moved[0].Payload)
	if content, _ := moved[0].Payload["content"].(string); !strings.Contains(content, "para renomear") {
		t.Fatalf("ponto copiado sem o conteúdo esperado: %q", content)
	}
	if cnt, err := q.Count(ctx); err != nil {
		t.Fatal(err)
	} else if cnt != int64(n2) {
		t.Fatalf("contagem no Qdrant (%d) deveria bater com o total (%d)", cnt, n2)
	}
}

func TestIntegrationDuplicateFileReusesVectors(t *testing.T) {
	q := newIntegrationQdrant(t)
	ctx := context.Background()
	dir := t.TempDir()
	e := &fakeEmbed{dims: 8}
	project := "dupe-proj"

	content := "# Doc\nconteudo duplicado entre arquivos\n"
	writeFile(t, dir, "a.md", content)
	n1, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: true})
	if err != nil {
		t.Fatalf("run1 (clean): %v", err)
	}
	callsAfterRun1 := e.numCalls()

	// b.md idêntico: todos os chunks copiam vetores, zero embeds novos.
	writeFile(t, dir, "b.md", content)
	n2, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: false})
	if err != nil {
		t.Fatalf("run2 (duplicata): %v", err)
	}
	if n2 != 2*n1 {
		t.Fatalf("esperava dobrar os chunks (%d → %d), veio %d", n1, 2*n1, n2)
	}
	if got := e.numCalls(); got != callsAfterRun1 {
		t.Fatalf("duplicata exata não deveria reembedar nada, %d chamadas novas de embed", got-callsAfterRun1)
	}
	if cnt, err := q.Count(ctx); err != nil {
		t.Fatal(err)
	} else if cnt != int64(n2) {
		t.Fatalf("contagem no Qdrant (%d) deveria bater com o total (%d)", cnt, n2)
	}
}
