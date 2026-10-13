// Package docgen gera documentação legível por arquivo (resumo, API pública,
// dependências) via LLM local — a segunda capacidade do serviço, além da
// indexação para busca. Geração e embedding usam modelos locais
// independentes (ex.: gerar com qwen2.5-coder e embutir com nomic-embed-text).
package docgen

import (
	"context"
	"fmt"
)

// Provider abstrai a origem das completions de documentação.
type Provider interface {
	// Generate pede uma completion de texto a partir de um prompt.
	Generate(ctx context.Context, prompt string) (string, error)
	Name() string
}

// Defaults da v1 (validados no spike de 2026-10-07):
//   - qwen2.5-coder:7b local gera doc correta em PT sem inventar;
//   - arquivos acima de MaxBytes são pulados (não cabem no contexto local).
const (
	DefaultTemperature = 0.2
	DefaultNumPredict  = 1024
	DefaultNumCtx      = 8192
	DefaultMaxBytes    = 30_000
)

// PromptFor monta o prompt de documentação de um arquivo (rascunho validado
// no spike — ajustar com teste real se o formato degradar).
func PromptFor(project, path, language, content string) string {
	return fmt.Sprintf(`Você está documentando um arquivo de código-fonte para um RAG interno.
Projeto: %s  Caminho: %s  Linguagem: %s

Escreva em português, de forma objetiva, cobrindo:
1. Propósito (1-2 frases): o que este arquivo faz e por quê existe.
2. API pública: funções/classes/exports relevantes para quem usa este
   arquivo de fora.
3. Dependências/colaboradores: o que ele importa/chama que é relevante
   para entender o fluxo.
4. Observações: comportamento não-óbvio, TODOs, limitações conhecidas —
   só se houver algo genuinamente não-óbvio, não invente.

Não inclua segredos, chaves ou credenciais mesmo que apareçam no arquivo.
Se o arquivo for puro dado/config sem lógica, diga isso em uma frase e pare.

Conteúdo do arquivo:
---
%s
---`, project, path, language, content)
}
