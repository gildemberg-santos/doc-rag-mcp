package embed

import "testing"

func TestNormalizeProviderDefaultsToOpenAI(t *testing.T) {
	got, err := NormalizeProvider("")
	if err != nil {
		t.Fatal(err)
	}
	if got != ProviderOpenAI {
		t.Fatalf("vazio deveria default pra %q, veio %q", ProviderOpenAI, got)
	}
}

func TestNormalizeProviderAcceptsKnownValuesCaseInsensitive(t *testing.T) {
	cases := map[string]string{
		"openai":   ProviderOpenAI,
		"OpenAI":   ProviderOpenAI,
		" openai ": ProviderOpenAI,
		"ollama":   ProviderOllama,
		"OLLAMA":   ProviderOllama,
	}
	for in, want := range cases {
		got, err := NormalizeProvider(in)
		if err != nil {
			t.Fatalf("NormalizeProvider(%q): erro inesperado: %v", in, err)
		}
		if got != want {
			t.Errorf("NormalizeProvider(%q) = %q, esperava %q", in, got, want)
		}
	}
}

func TestNormalizeProviderRejectsUnknown(t *testing.T) {
	_, err := NormalizeProvider("anthropic")
	if err == nil {
		t.Fatal("esperava erro pra provider desconhecido")
	}
}

func TestCollectionForOpenAIPreservesBase(t *testing.T) {
	if got := CollectionFor(ProviderOpenAI, "docs"); got != "docs" {
		t.Fatalf("openai não deveria alterar a coleção base, veio %q", got)
	}
}

func TestCollectionForOllamaAppendsSuffix(t *testing.T) {
	if got := CollectionFor(ProviderOllama, "docs"); got != "docs-ollama" {
		t.Fatalf("esperava %q, veio %q", "docs-ollama", got)
	}
}

func TestCollectionForOllamaAvoidsDoubleSuffix(t *testing.T) {
	if got := CollectionFor(ProviderOllama, "docs-ollama"); got != "docs-ollama" {
		t.Fatalf("não deveria duplicar o sufixo, veio %q", got)
	}
}

func TestCollectionForDefaultsEmptyBaseToDocs(t *testing.T) {
	if got := CollectionFor(ProviderOpenAI, ""); got != "docs" {
		t.Fatalf("base vazia deveria default pra %q, veio %q", "docs", got)
	}
	if got := CollectionFor(ProviderOllama, ""); got != "docs-ollama" {
		t.Fatalf("base vazia + ollama deveria virar %q, veio %q", "docs-ollama", got)
	}
}
