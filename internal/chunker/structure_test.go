package chunker

import (
	"strings"
	"testing"
)

const rubySample = `# frozen_string_literal: true

module Bagy
  class BaseService
    def self.call(params)
      new(params).call
    end

    def call
      return Success(webhook) when ok?
      Failure(:invalid)
    end

    private

    def ok?
      true
    end
  end
end
`

func TestSplitStructuredRuby(t *testing.T) {
	blocks := splitStructured(rubySample, "rb")
	if len(blocks) < 3 {
		t.Fatalf("esperava >=3 blocos (module/class/defs), veio %d", len(blocks))
	}
	// defs dentro da classe herdam o escopo da classe
	foundClassScope := false
	for _, b := range blocks {
		if strings.Contains(b.scope, "class BaseService") {
			foundClassScope = true
		}
		if b.body == "" {
			t.Fatal("bloco vazio")
		}
	}
	if !foundClassScope {
		t.Fatalf("nenhum bloco com escopo da classe: %+v", blocks)
	}
}

func TestSplitStructuredMarkdown(t *testing.T) {
	md := "# Título\n\ntexto\n\n## Seção A\n\nconteúdo A\n\n## Seção B\n\nconteúdo B\n"
	blocks := splitStructured(md, "md")
	if len(blocks) != 3 {
		t.Fatalf("esperava 3 blocos (título + 2 seções), veio %d", len(blocks))
	}
	if !strings.Contains(blocks[1].scope, "Seção A") || !strings.Contains(blocks[1].body, "conteúdo A") {
		t.Fatalf("bloco da seção A errado: %+v", blocks[1])
	}
}

func TestSplitStructuredFallback(t *testing.T) {
	if splitStructured("a: 1\nb: 2\n", "yml") != nil {
		t.Fatal("yml deveria usar fallback (nil)")
	}
	if splitStructured("x", "rb") == nil {
		t.Fatal("rb sem fronteiras deveria gerar 1 bloco, não nil")
	}
}

func TestPackBlocksRespectsSize(t *testing.T) {
	blocks := splitStructured(rubySample, "rb")
	packed := packBlocks(blocks, 200)
	for _, p := range packed {
		if len(p.body) > 1200 { // bloco gigante fatiado sempre cabe em size+linha
			t.Fatalf("pedaço estourou demais: %d chars", len(p.body))
		}
		if p.scope == "" {
			t.Fatal("pedaço sem escopo")
		}
	}
	// Nenhum pedaço deveria repetir conteúdo do anterior (sem overlap)
	seen := map[string]bool{}
	for _, p := range packed {
		for _, ln := range strings.Split(p.body, "\n") {
			ln = strings.TrimSpace(ln)
			if len(ln) < 20 {
				continue
			}
			if seen[ln] {
				t.Fatalf("linha duplicada entre pedaços (overlap indevido): %q", ln)
			}
			seen[ln] = true
		}
	}
}
