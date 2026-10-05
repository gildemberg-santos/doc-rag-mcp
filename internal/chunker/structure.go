package chunker

// Chunking por blocos de estrutura (mitiga o item 3 da análise MCP-vs-grep).
//
// Em vez de fatiar por janela fixa de caracteres (que corta métodos no meio
// e repete trechos via overlap), aqui dividimos por fronteiras lógicas:
// classe/módulo/método (Ruby, JS/TS) e headings (Markdown). Cada bloco carrega
// o escopo (ex.: "class Foo") para que o chunk nunca perca o contexto —
// o problema observado onde o resultado #6 não mostrava ser da mesma classe
// que o #1.
//
// Linguagens sem regra usam o fallback de janela (splitIntoChunks).
import (
	"regexp"
	"strings"
)

// block é uma unidade lógica do arquivo com seu escopo.
type block struct {
	scope string // ex.: "class ProcessWebhookService" ("" se nenhum)
	body  string
}

var (
	rbBoundary = regexp.MustCompile(`^\s*(class\s+\S+|module\s+\S+|def\s+\S+|describe\s+.*|context\s+.*)\b`)
	rbScope    = regexp.MustCompile(`^\s*(class\s+\S+|module\s+\S+)`)
	jsBoundary = regexp.MustCompile(`^\s*(export\s+)?(async\s+)?(function\s+\w+|class\s+\w+|interface\s+\w+|enum\s+\w+|type\s+\w+|const\s+\w+\s*=)`)
	jsScope    = regexp.MustCompile(`^\s*(export\s+)?(async\s+)?(class\s+\w+|function\s+\w+)`)
	mdBoundary = regexp.MustCompile(`^#{1,3}\s+\S`)
)

// splitStructured divide o texto em blocos lógicos.
// Retorna nil se a linguagem não tem regra (chamador usa fallback).
func splitStructured(text, language string) []block {
	var boundary, scopeRe *regexp.Regexp
	switch language {
	case "rb", "rake":
		boundary, scopeRe = rbBoundary, rbScope
	case "js", "jsx", "ts", "tsx":
		boundary, scopeRe = jsBoundary, jsScope
	case "md", "mdx":
		boundary = mdBoundary
	default:
		return nil
	}

	var blocks []block
	var cur strings.Builder
	curScope := ""
	blockScope := ""
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		blocks = append(blocks, block{scope: blockScope, body: cur.String()})
		cur.Reset()
	}
	for _, ln := range strings.Split(text, "\n") {
		if boundary.MatchString(ln) {
			flush()
			if language == "md" {
				curScope = strings.TrimSpace(ln)
			} else if scopeRe != nil && scopeRe.MatchString(ln) {
				curScope = strings.TrimSpace(ln)
			}
			// métodos/funções herdam o escopo da classe; sem classe
			// (ou em md), o próprio bloco vira o escopo.
			blockScope = curScope
			// métodos/funções herdam o escopo da classe; sem classe, o próprio def.
			if blockScope == "" {
				blockScope = strings.TrimSpace(ln)
				if len(blockScope) > 100 {
					blockScope = blockScope[:100]
				}
			}
		}
		cur.WriteString(ln)
		cur.WriteString("\n")
	}
	flush()
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

// packBlocks empacota blocos em pedaços de até size chars.
// Bloco maior que size é fatiado por linhas. Cada pedaço sai com seu escopo.
func packBlocks(blocks []block, size int) []block {
	var out []block
	var cur strings.Builder
	curScope := ""
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		out = append(out, block{scope: curScope, body: cur.String()})
		cur.Reset()
		curScope = ""
	}
	for _, b := range blocks {
		if len(b.body) > size {
			flush()
			// fatia bloco gigante por linhas
			var acc strings.Builder
			for _, ln := range strings.Split(b.body, "\n") {
				if acc.Len()+len(ln)+1 > size {
					out = append(out, block{scope: b.scope, body: acc.String()})
					acc.Reset()
				}
				acc.WriteString(ln)
				acc.WriteString("\n")
			}
			if acc.Len() > 0 {
				out = append(out, block{scope: b.scope, body: acc.String()})
			}
			continue
		}
		if cur.Len()+len(b.body) > size {
			flush()
		}
		if curScope == "" {
			curScope = b.scope
		}
		cur.WriteString(b.body)
	}
	flush()
	return out
}
