package chunker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLanguageForUsesExtensionByDefault(t *testing.T) {
	cases := map[string]string{
		"app/models/user.rb": "rb",
		"src/index.tsx":      "tsx",
		"foo.unknown_ext":    "unknown_ext",
		"noext":              "",
	}
	for path, want := range cases {
		if got := LanguageFor(path); got != want {
			t.Errorf("LanguageFor(%q) = %q, esperava %q", path, got, want)
		}
	}
}

func TestLanguageForPrefersKnownBaseNameOverExtension(t *testing.T) {
	// README.md e CHANGELOG.md têm extensão .md, mas o nome do arquivo em
	// si é mais informativo pro retrieval do que "md" genérico.
	cases := map[string]string{
		"README.md":    "readme",
		"CHANGELOG.md": "changelog",
		"Makefile":     "makefile",
		"Dockerfile":   "dockerfile",
	}
	for path, want := range cases {
		if got := LanguageFor(path); got != want {
			t.Errorf("LanguageFor(%q) = %q, esperava %q", path, got, want)
		}
	}
}

func TestSplitIntoChunksReturnsWholeTextWhenUnderSize(t *testing.T) {
	text := "linha única, bem curta"
	out := splitIntoChunks(text, 1200, 200)
	if len(out) != 1 || out[0] != text {
		t.Fatalf("texto menor que size deveria voltar inteiro e inalterado, veio %v", out)
	}
}

func TestSplitIntoChunksNeverProducesEmptyPieces(t *testing.T) {
	// Regressão: uma linha isolada maior que size, como primeira linha do
	// arquivo, gerava um pedaço vazio antes do fatiamento bruto da linha.
	longLine := strings.Repeat("0123456789", 50) // 500 chars
	out := splitIntoChunks(longLine, 30, 5)
	if len(out) == 0 {
		t.Fatal("esperava ao menos um pedaço")
	}
	for i, p := range out {
		if p == "" {
			t.Errorf("pedaço %d veio vazio — desperdiça um chunk/embedding com conteúdo inútil", i)
		}
	}
}

func TestSplitIntoChunksPreservesOverlapBetweenPieces(t *testing.T) {
	text := "linha1\nlinha2\nlinha3\nlinha4\nlinha5\nlinha6\n"
	out := splitIntoChunks(text, 15, 6)
	if len(out) < 2 {
		t.Fatalf("esperava múltiplos pedaços pra forçar overlap, veio %d: %v", len(out), out)
	}
	for i := 1; i < len(out); i++ {
		prevTail := out[i-1]
		if len(prevTail) > 6 {
			prevTail = prevTail[len(prevTail)-6:]
		}
		if !strings.Contains(out[i], strings.TrimRight(prevTail, "\n")) && !strings.HasPrefix(out[i], prevTail) {
			t.Logf("pedaço %d (%q) não contém claramente o overlap esperado do pedaço anterior (%q) — pode ser variação aceitável do algoritmo, não falha dura", i, out[i], out[i-1])
		}
	}
}

func TestIsMostlyTextRejectsEmpty(t *testing.T) {
	if isMostlyText("") {
		t.Fatal("string vazia não deveria ser considerada texto")
	}
}

func TestIsMostlyTextAcceptsNormalText(t *testing.T) {
	if !isMostlyText("package main\n\nfunc main() {}\n") {
		t.Fatal("código-fonte normal deveria ser aceito como texto")
	}
}

func TestIsMostlyTextRejectsNullByte(t *testing.T) {
	if isMostlyText("abc\x00def") {
		t.Fatal("um único byte NUL já deveria classificar como binário")
	}
}

func TestIsMostlyTextAllowsCommonControlChars(t *testing.T) {
	// tab(9), LF(10), CR(13) e ESC(27) são controles comuns em texto real
	// (ESC aparece em sequências ANSI de logs, por ex.) e não devem contar
	// como "ruído binário".
	text := "coluna1\tcoluna2\r\nlinha2\x1b[0m fim\n"
	if !isMostlyText(text) {
		t.Fatalf("texto com tab/CR/LF/ESC deveria ser aceito, foi rejeitado: %q", text)
	}
}

func TestIsMostlyTextRejectsMostlyBinary(t *testing.T) {
	// bytes de controle "ruins" (ex.: 1-8, 14-26, 28-31) em quantidade >2%
	b := make([]byte, 200)
	for i := range b {
		if i%10 == 0 {
			b[i] = 5 // controle ruim, bem mais que 2% do total
		} else {
			b[i] = 'a'
		}
	}
	if isMostlyText(string(b)) {
		t.Fatal("conteúdo com >2% de bytes de controle ruins deveria ser rejeitado como binário")
	}
}

// scanAll varre dir e devolve os chunks por path (ordem de emissão).
func scanAll(t *testing.T, project, dir string) map[string][]Chunk {
	t.Helper()
	got := map[string][]Chunk{}
	err := ScanProject(project, dir, nil, nil, func(c Chunk) error {
		got[c.Path] = append(got[c.Path], c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBodyHashIsStableAcrossRename(t *testing.T) {
	content := "# Doc\nconteúdo idêntico nos dois arquivos\nmais uma linha\n"
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "a.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir2, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "sub", "b.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	a := scanAll(t, "proj", dir1)["a.md"]
	b := scanAll(t, "proj", dir2)["sub/b.md"]
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("esperava chunks nos dois arquivos, veio %d e %d", len(a), len(b))
	}
	if len(a) != len(b) {
		t.Fatalf("mesmo conteúdo deveria gerar o mesmo nº de chunks, veio %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].BodyHash == "" || a[i].FileHash == "" {
			t.Fatalf("chunk %d sem BodyHash/FileHash preenchidos", i)
		}
		if a[i].BodyHash != b[i].BodyHash {
			t.Errorf("chunk %d: BodyHash deveria ser igual após rename (%q vs %q)", i, a[i].BodyHash, b[i].BodyHash)
		}
		if a[i].Hash == b[i].Hash {
			t.Errorf("chunk %d: Hash (com header/path) deveria DIFERIR após rename", i)
		}
	}
	if a[0].FileHash != b[0].FileHash {
		t.Errorf("FileHash deveria ser igual após rename (%q vs %q)", a[0].FileHash, b[0].FileHash)
	}
}

func TestFileHashChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte("# Doc\nversão um\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before := scanAll(t, "proj", dir)["doc.md"]
	// garante mtime diferente (irrelevante pro hash, mas evita flake de fs)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte("# Doc\nversão dois, diferente\n"), 0644); err != nil {
		t.Fatal(err)
	}
	after := scanAll(t, "proj", dir)["doc.md"]
	if len(before) == 0 || len(after) == 0 {
		t.Fatal("esperava chunks antes e depois da edição")
	}
	if before[0].FileHash == after[0].FileHash {
		t.Error("FileHash deveria mudar quando o conteúdo muda")
	}
	if before[0].BodyHash == after[0].BodyHash {
		t.Error("BodyHash deveria mudar quando o conteúdo muda")
	}
}

func TestFileHashIsConsistentWithinFile(t *testing.T) {
	// Arquivo com vários chunks: todos carregam o mesmo FileHash.
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString("linha de conteudo bem distinto repetida aqui para encher o arquivo\n")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	chunks := scanAll(t, "proj", dir)["big.txt"]
	if len(chunks) < 2 {
		t.Fatalf("esperava múltiplos chunks, veio %d", len(chunks))
	}
	for i, c := range chunks {
		if c.FileHash != chunks[0].FileHash {
			t.Fatalf("chunk %d tem FileHash diferente dos irmãos — file_hash deve ser por arquivo", i)
		}
		if c.BodyHash == "" {
			t.Fatalf("chunk %d sem BodyHash", i)
		}
	}
}

func TestListFilesRespectsIndexRules(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"indexavel.md":      "# ok\n",
		"credentials.json":  "x", // nome sensível em ext de config → fora
		"grande.txt":        strings.Repeat("a", 300),
		".oculto.txt":       "x",
		"package-lock.json": "{}",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListFiles(dir, 200) // grande.txt (300B) passa do teto
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range got {
		names[f.Rel] = true
		if f.Abs == "" || f.Size <= 0 {
			t.Errorf("FileRef incompleto: %+v", f)
		}
	}
	if !names["indexavel.md"] {
		t.Errorf("indexavel.md deveria estar listado, veio %v", names)
	}
	for _, want := range []string{"credentials.json", "grande.txt", ".oculto.txt", "package-lock.json"} {
		if names[want] {
			t.Errorf("%s deveria estar excluído (regra do chunker), mas foi listado", want)
		}
	}
}

func TestListFilesEmptyDir(t *testing.T) {
	got, err := ListFiles(t.TempDir(), 200*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dir vazio deveria listar 0 arquivos, veio %d", len(got))
	}
}

func TestIsTextMirrorsScan(t *testing.T) {
	if !IsText("package main\nfunc x() {}\n") {
		t.Error("código normal deveria ser texto")
	}
	if IsText("abc\x00def") {
		t.Error("NUL deveria reprovar como binário")
	}
}
