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
	"testing"
	"time"

	"doc-rag-mcp/internal/vector"
)

// Testes de integração contra um Qdrant real — não rodam no `go test ./...`
// normal. Pra rodar: suba um Qdrant (ex. `docker compose up -d qdrant`) e
// chame `make test-integration` (ou RUN_QDRANT_INTEGRATION=1 go test ./internal/indexer/... -run Integration -v).
//
// Cobrem o que só dá pra validar com um Qdrant de verdade: diff incremental
// por hash/mtime, streaming com persistência parcial, e o "self-heal" de
// uma atualização interrompida no meio — tudo isso foi validado manualmente
// em sessões anteriores (escrevendo e apagando o teste a cada vez); aqui
// fica formalizado como teste permanente, só não roda por padrão.

type fakeEmbed struct {
	calls     int
	dims      int
	failAfter int // se >0, falha a partir da (failAfter+1)-ésima chamada a Embed
}

func (f *fakeEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.failAfter > 0 && f.calls > f.failAfter {
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

// newIntegrationQdrant pula o teste se RUN_QDRANT_INTEGRATION não estiver
// setado, e devolve um client numa coleção descartável só desse teste
// (apagada no cleanup).
func newIntegrationQdrant(t *testing.T) *vector.Client {
	t.Helper()
	if os.Getenv("RUN_QDRANT_INTEGRATION") == "" {
		t.Skip("defina RUN_QDRANT_INTEGRATION=1 (com um Qdrant local acessível) para rodar testes de integração")
	}
	url := os.Getenv("QDRANT_URL")
	if url == "" {
		url = "http://localhost:6333"
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
	_, err = RunWithOptions(ctx, project, dir, q, eFail, Options{Clean: false, EmbedBatchSize: 1})
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
	_, err := RunWithOptions(ctx, project, dir, q, e, Options{Clean: true, EmbedBatchSize: 1})
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
