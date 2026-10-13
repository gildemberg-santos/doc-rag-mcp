package embed

import (
	"strings"
	"testing"
)

func TestNormalizeProviderDefaultsToOllama(t *testing.T) {
	got, err := NormalizeProvider("")
	if err != nil {
		t.Fatal(err)
	}
	if got != ProviderOllama {
		t.Fatalf("vazio deveria default pra %q, veio %q", ProviderOllama, got)
	}
}

func TestNormalizeProviderAcceptsOllamaCaseInsensitive(t *testing.T) {
	for _, in := range []string{"ollama", "OLLAMA", " ollama "} {
		got, err := NormalizeProvider(in)
		if err != nil {
			t.Fatalf("NormalizeProvider(%q): erro inesperado: %v", in, err)
		}
		if got != ProviderOllama {
			t.Errorf("NormalizeProvider(%q) = %q, esperava %q", in, got, ProviderOllama)
		}
	}
}

func TestNormalizeProviderRejectsOpenAIWithExplicitError(t *testing.T) {
	_, err := NormalizeProvider("openai")
	if err == nil {
		t.Fatal("esperava erro pra openai (removido)")
	}
	if got := err.Error(); !strings.Contains(got, "removido") {
		t.Errorf("erro deveria dizer que foi removido, veio: %v", err)
	}
}

func TestNormalizeProviderRejectsUnknown(t *testing.T) {
	_, err := NormalizeProvider("anthropic")
	if err == nil {
		t.Fatal("esperava erro pra provider desconhecido")
	}
}
