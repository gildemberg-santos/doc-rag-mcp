# Plano: geração automática de documentação por arquivo

> Status: planejamento, nada implementado ainda. Este documento existe para
> alinhar decisões antes de abrir issues/PRs. Atualize-o conforme decisões
> forem tomadas ou revertidas — não é um snapshot congelado.

## Problema

Hoje o serviço só faz **indexação para busca** (`internal/chunker` → chunks →
`internal/embed` → vetores → `internal/vector`/Qdrant), exposta via
`search_docs`/`grep_docs`/`get_document`. Não existe nenhuma etapa que gere
**documentação legível** (resumo, propósito, API pública, dependências) de
cada arquivo — quem quer entender um arquivo precisa ler o código bruto ou os
trechos ranqueados pelo `search_docs`.

Objetivo: adicionar uma segunda capacidade, **geração automática de
documentação por arquivo**, mantida em sincronia com o ciclo de indexação
existente, servida pelas mesmas tools MCP (ou novas tools complementares).

## Não-objetivos (por agora)

- **Não** escrever/injetar a documentação gerada de volta no código-fonte do
  usuário (ex.: inserir docstring no topo do arquivo). Mexer no checkout de
  outro projeto é um efeito colateral arriscado e fora do escopo deste
  serviço, que hoje é só leitura sobre `/projects`. Se vier a ser pedido,
  tratar como feature separada, com flag opt-in explícita e provavelmente
  via PR em vez de escrita direta.
- **Não** substituir `search_docs`/`grep_docs` — a documentação gerada é uma
  camada adicional, não troca os chunks brutos (eles continuam necessários
  pra responder "mostra o código que faz X").
- **Não** gerar documentação para todo `--all` por padrão na v1 — ver
  "Custo e escopo" abaixo.

## Desenho proposto

### 1. Nova interface de geração (não é embedding)

`internal/embed.Provider` hoje só expõe `Embed(texts) ([][]float32, error)` —
serve pra vetores, não pra texto gerado. Precisa de uma interface nova,
separada:

```go
// internal/docgen/docgen.go
package docgen

type Provider interface {
    Name() string
    // Generate pede uma completion de chat/texto a partir de um prompt.
    Generate(ctx context.Context, prompt string) (string, error)
}
```

Implementações espelhando `internal/embed/openai.go` e `internal/embed/ollama.go`:
- `internal/docgen/openai.go` — Chat Completions API (`OPENAI_DOCGEN_MODEL`,
  default algo como `gpt-4o-mini` — barato, não precisa do modelo mais caro
  pra resumir um arquivo).
- `internal/docgen/ollama.go` — `/api/generate` ou `/api/chat` (`OLLAMA_DOCGEN_MODEL`,
  ex.: `qwen2.5-coder` ou o que já estiver disponível localmente — mesma
  filosofia híbrida do embedding: Ollama = grátis/offline, OpenAI = produção).

Importante: o provider de **docgen** é independente do provider de
**embedding** — pode-se gerar doc com Ollama mesmo indexando com OpenAI, ou
vice-versa. Não assumir que são o mesmo `EMBED_PROVIDER`.

### 2. Granularidade: por arquivo, não por chunk

O prompt recebe o **conteúdo completo do arquivo** (reaproveitando o mesmo
scan do `internal/chunker`, respeitando `MaxFileBytes`), não um chunk
isolado — documentação de um trecho de 1200 chars sem o resto do arquivo
tende a ser genérica/errada.

Para arquivos que excedem a janela de contexto do modelo (pouco provável com
`MaxFileBytes=200KB`, mas possível): fase 2, estratégia map-reduce — gerar um
resumo por chunk e depois um prompt de síntese sobre os resumos. Não
resolver isso na v1; na v1, arquivos grandes demais pro prompt do provider
simplesmente ficam sem doc gerada (log de aviso, não erro fatal).

### 3. Armazenamento: nova coleção Qdrant, não mistura com os chunks

Guardar como **pontos separados** numa coleção própria (`<collection>-filedocs`,
ex. `docs-ollama-filedocs`), um ponto por arquivo:

```json
{
  "project": "atendimento",
  "path": "app/services/webhook_dispatcher.rb",
  "language": "rb",
  "doc_content": "## Propósito\n...\n## API pública\n...\n## Dependências\n...",
  "source_hash": "sha256 do conteúdo do arquivo no momento da geração",
  "model": "gpt-4o-mini",
  "generated_at": "2026-10-06T13:00:00Z"
}
```

O vetor desse ponto é o **embedding do próprio `doc_content`** (reaproveita
`internal/embed`) — isso permite busca semântica sobre a documentação gerada
(pergunta em linguagem natural → bate com o resumo, não com código ruidoso),
sem poluir o ranking de `search_docs` que já existe sobre os chunks brutos.

Por que coleção separada em vez de um campo novo no payload dos chunks
existentes: `search_docs` já tem lógica de diversidade por arquivo e peso
keyword vs vetor calibrada para chunks de código; misturar um "super-chunk"
de documentação no mesmo espaço mudaria esse ranking de forma sutil. Separar
mantém as duas features independentes e testáveis isoladamente.

### 4. Sincronia incremental (reaproveitar o padrão já existente)

O indexer já faz diff por hash de conteúdo pra pular chunks inalterados
(`internal/indexer/indexer.go`, ver `pointID`/diff). Docgen replica a mesma
ideia na granularidade de arquivo:

- Hash do arquivo inteiro (não por chunk) vira `source_hash`.
- Antes de chamar o LLM, comparar com o `source_hash` já salvo pra aquele
  `project+path` — se igual, pula (sem custo de completion).
- Arquivo novo/alterado → gera; arquivo removido → remove o ponto
  correspondente (mesma lógica de limpeza que já existe pros chunks).

Isso é o que torna viável rodar docgen como parte do `--watch` sem custo
crescente: só paga completion pra diffs reais, igual ao embedding hoje.

### 5. Novo modo de execução

Opção A (mais simples): flag no `cmd/indexer` existente, `--generate-docs`
(ou `DOCGEN_ENABLED=true`), roda como uma etapa extra depois do
scan+embed+upsert de cada projeto, dentro do mesmo processo.

Opção B: binário separado `cmd/docgen`, ciclo próprio, próprio
`--interval`/`--watch`. Mais isolamento operacional (pode travar/ser lento
sem afetar o ciclo de embedding, que é crítico pra busca funcionar), mas mais
um container pra manter no `docker-compose.yml`.

**Recomendação:** começar pela Opção A (menos infra nova), e só migrar pra B
se na prática o docgen (completions, mais lento que embedding — ver seção de
custo) começar a atrasar o ciclo de embedding que hoje roda em ~10min.

### 6. Novas tools MCP

Em `internal/mcpserver/tools.go`, seguindo o padrão das tools existentes:

| Tool | Descrição |
|---|---|
| `get_file_doc(project, path)` | Retorna a documentação gerada de um arquivo (ou "ainda não gerada" se não existir ponto correspondente). |
| `search_file_docs(query, project?, top_k?)` | Busca semântica sobre as documentações geradas — melhor que `search_docs` pra "o que esse módulo faz", porque busca em prosa, não em código. |
| `list_docs` (estender) | Adicionar `has_doc: bool` por arquivo listado. |
| `index_status` (estender) | Reportar cobertura: "X/Y arquivos têm doc gerada" por projeto, além do que já reporta pro embedding. |

Todas as respostas de doc gerada devem deixar explícito no texto que é
**conteúdo gerado por LLM, pode conter erros** — mesmo cuidado que
`search_docs` já tem ao rotular força de relevância.

### 7. Custo e escopo (o maior risco do plano)

Completions são mais caras/lentas que embeddings — hoje mesmo observamos o
reembed completo de `atendimento` (37.831 chunks) levando horas via Ollama
local em lotes de 64. Gerar uma completion por **arquivo** é mais barato em
contagem de chamadas (menos arquivos que chunks), mas cada chamada é mais
cara em tokens de saída e mais lenta.

Decisões pra v1:
- **Opt-in por projeto**, nunca `--all` automático: `--generate-docs
  --project atendimento` explícito, não ligado por padrão no `--watch` até
  medir custo real num projeto pequeno primeiro (`doc-rag-mcp`, 291 chunks,
  é o candidato óbvio pro piloto).
- **Concorrência limitada** (semáforo, ex. 2-4 chamadas simultâneas) — não
  disparar uma goroutine por arquivo sem controle.
- **Filtrar arquivos óbvios**: pular o que já é fraco candidato a precisar de
  doc gerada — arquivos de config/dados puros (`.json`, `.yml` sem lógica),
  lockfiles (já excluídos pelo chunker), arquivos muito pequenos (ex. <20
  linhas) onde o código já é autoexplicativo.
- Reportar no `/status`/`index_status` o custo acumulado estimado (nº de
  chamadas de docgen no ciclo), pra dar visibilidade antes de escalar pra
  mais projetos.

### 8. Prompt (rascunho inicial, ajustar com teste real)

```
Você está documentando um arquivo de código-fonte para um RAG interno.
Projeto: {project}  Caminho: {path}  Linguagem: {language}

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
{conteúdo}
---
```

A mesma regra de exclusão de segredos que já existe no chunker
(`internal/chunker/chunker.go`, `sensitiveBaseNames`/`configLikeExts`) deve
valer aqui: arquivos marcados como sensíveis não entram no pipeline de
docgen, igual já não entram no de embedding.

## Fases de implementação

1. **Spike manual** — script/CLI mínimo chamando `docgen.Provider` sobre os
   291 chunks de `doc-rag-mcp` (o próprio repo), validar prompt e formato de
   saída antes de qualquer coleção/tool nova.
2. **MVP** — `internal/docgen` (interface + OpenAI/Ollama), nova coleção,
   flag `--generate-docs` no `cmd/indexer`, tools `get_file_doc` e
   `search_file_docs`. Sem incremental ainda (sempre regenera) — só pra
   validar o pipeline fim a fim num projeto pequeno.
3. **Incremental + custo controlado** — diff por `source_hash`, concorrência
   limitada, filtros de arquivo, relato em `/status`.
4. **Integração com `--watch`** — docgen passa a rodar no ciclo periódico
   (projetos opt-in via config), com intervalo próprio (provavelmente maior
   que os 10min do embedding — ex. diário).
5. **(Futuro, fora deste plano)** — eventual escrita de volta no
   código-fonte, geração de um README por diretório agregando os docs de
   arquivo, etc. Não iniciar sem validar as fases 1-4 primeiro.

## Abertas / decisões pendentes

- Modelo default de docgen (OpenAI e Ollama) — decidir depois do spike
  manual, com base em qualidade/custo observados, não a priori.
- Opção A vs B da seção 5 (dentro do indexer vs binário próprio) — decidir
  depois do MVP, com dados reais de quanto o docgen atrasa o ciclo.
- Se `search_file_docs` deve ter diversidade por arquivo como `search_docs`
  — provavelmente não faz sentido (já é 1 ponto por arquivo), mas revisar
  quando a tool existir.
