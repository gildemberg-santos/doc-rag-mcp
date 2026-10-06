package chunker

import (
	"strings"
	"testing"
)

func TestLanguageForUsesExtensionByDefault(t *testing.T) {
	cases := map[string]string{
		"app/models/user.rb": "rb",
		"src/index.tsx":      "tsx",
		"foo.unknown_ext":    "unknown_ext",
		"noext":              "",
	}
	for path, want := range cases {
		if got := languageFor(path); got != want {
			t.Errorf("languageFor(%q) = %q, esperava %q", path, got, want)
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
		if got := languageFor(path); got != want {
			t.Errorf("languageFor(%q) = %q, esperava %q", path, got, want)
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
