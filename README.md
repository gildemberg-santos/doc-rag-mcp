# doc-rag-mcp — MCP de Documentação com RAG (Go + Qdrant + Ollama, 100% local)

Servidor **MCP** que expõe a documentação/código dos seus projetos via **RAG**:
escaneia os repositórios → fatia em chunks → gera embeddings (**Ollama
local**, sem API externa, sem key) → salva no **Qdrant** (local, via Docker)
→ serve busca semântica como **tools MCP** para qualquer LLM (Claude Code,
Opencode, Cursor, etc).

## Arquitetura

```
┌──────────────┐   indexa    ┌──────────┐  embeddings  ┌───────────┐
│  neurolead   │ ──────────▶ │ indexer  │ ───────────▶ │  Qdrant   │
│  neurolead-  │   chunks    │ (Go CLI) │  Ollama     │ (Docker,  │
│  client      │             │          │  nomic-     │  :6333)   │
└──────────────┘             └──────────┘  embed-text └─────┬─────┘
                                                              │ busca
┌────────────┐  MCP tools   ┌───────────────┐  query vec     │
│ Claude     │ ◀─────────── │  mcp-server   │ ─────────────┘
│ Opencode   │  stdio ou    │  (Go, tools:  │
│ ...        │  Streamable  │  search_docs, │
└────────────┘  HTTP /mcp   │  get_document,│
                            │  list_*, ...) │
                            └───────────────┘
```

**Tools MCP expostas:**

| Tool | Descrição |
|---|---|
| `search_docs` | Busca semântica + keyword (`query`, `project?` csv cross-project, `top_k?` máx 50, `diversify?`, `max_per_file?`, `path_prefix?`, `language?`, `pure_vector?`) |
| `grep_docs` | Inventário literal: todos os arquivos com o termo + ocorrências (`pattern`, `project?`, `path_prefix?`, `limit?` máx 500) |
| `get_document` | Arquivo completo (`project`, `path`, `offset?`, `limit?` máx 500 — pagina arquivos grandes em vez de despejar 200KB de uma vez) |
| `get_file_doc` | Documentação gerada por LLM de um arquivo (`project`, `path`) — com aviso de que pode conter erros |
| `search_file_docs` | Busca semântica sobre as docs geradas (`query`, `project?`, `top_k?` máx 20, `path_prefix?`) — melhor para "o que esse módulo faz" |
| `list_projects` | Projetos + contagem + data da última indexação |
| `list_docs` | Arquivos de um projeto (`project`, `limit?`, `prefix?`) — marca `· doc gerada` quando há doc |
| `index_status` | Servidor (nome+versão), provider, coleção, dims, total de pontos + cobertura de docs geradas |

**Como pesquisar bem (lições da análise MCP-vs-grep):**
- `search_docs` = entender o padrão (ranking com diversidade por arquivo + match literal, força forte ≥0.55)
- `grep_docs` = inventário completo (ex.: 312 arquivos com "webhook" no índice vs 354 no `grep -ri` — o gap são arquivos fora das regras de indexação)
- Combine: `grep_docs` para mapear, `search_docs`/`get_document` para entender cada grupo

## Quickstart (Docker — recomendado)

```bash
cd doc-rag-mcp
cp .env.example .env   # sem key externa — tudo local
docker compose up -d --build     # sobe qdrant + ollama + mcp-server
docker compose exec ollama ollama pull nomic-embed-text   # ou: make ollama-pull
docker compose run --rm indexer --all   # indexa neurolead + neurolead-client
curl localhost:8080/health
curl "localhost:8080/search?q=como+funciona+autenticação"
# Qdrant UI: http://localhost:6333/dashboard
```

## Uso local (sem Docker, Qdrant ainda via Docker)

```bash
docker run -p 6333:6333 qdrant/qdrant:latest &  # ou docker compose up qdrant
cp .env.example .env
go mod tidy
go run ./cmd/indexer --all                      # PROJECTS_ROOT=/home/.../Documentos
go run ./cmd/server --transport http --http-addr :8080
```

## Conectar LLMs

### Claude Code (local stdio — precisa do binário + Qdrant acessível)

```bash
go build -o ~/.local/bin/doc-rag-mcp ./cmd/server
claude mcp add doc-rag -- ./bin... # exemplo:
claude mcp add doc-rag -- ~/.local/bin/doc-rag-mcp --transport stdio
# com env:
QDRANT_URL=http://localhost:6333 OLLAMA_URL=http://localhost:11434 ~/.local/bin/doc-rag-mcp --transport stdio
```

Ou via HTTP (servidor Docker rodando):

```bash
claude mcp add -t http doc-rag http://localhost:8080/mcp
```

### Opencode (`opencode.json`)

```json
{
  "mcp": {
    "doc-rag": {
      "type": "remote",
      "url": "http://localhost:8080/mcp",
      "enabled": true
    }
  }
}
```

Para stdio no opencode:

```json
{
  "mcp": {
    "doc-rag": {
      "type": "local",
      "command": ["/home/user/.local/bin/doc-rag-mcp", "--transport", "stdio"],
      "enabled": true,
      "environment": {
        "QDRANT_URL": "http://localhost:6333",
        "OLLAMA_URL": "http://localhost:11434"
      }
    }
  }
}
```

Veja `examples/opencode.json` e `examples/claude.json`.

### Qualquer cliente Streamable HTTP

Endpoint: `POST http://localhost:8080/mcp` (MCP Streamable HTTP).
Debug REST (sem MCP): `GET /search?q=...&project=...`.

## Reindexar

```bash
# tudo (coleção docs, Ollama local), incremental por padrão (só o diff):
docker compose run --rm indexer --all
# um projeto:
docker compose run --rm indexer --project neurolead --path /projects/neurolead
# reindex completo (apaga tudo antes — só para migrações de dims/modelo):
docker compose run --rm indexer --all --clean
# simular sem escrever nada (conta inalterados/a-embedar/obsoletos):
docker compose run --rm indexer --all --dry-run
```

> O modo padrão (incremental) compara o hash do conteúdo de cada chunk
> com o que já está no Qdrant: chunks inalterados são pulados (sem chamar o
> provider de embedding), só os novos/alterados são reembedados, e pontos de
> arquivos removidos/renomeados/encurtados são apagados automaticamente.
> Na primeira execução incremental após atualizar para essa versão, todo o
> índice é reembedado uma vez (os pontos antigos não têm o novo esquema de
> ID/hash) — isso também limpa qualquer duplicação deixada por versões
> anteriores do modo incremental.
>
> Trocar de modelo/dims (`OLLAMA_MODEL`, `OLLAMA_EMBED_DIMS`) sem recriar a
> coleção mistura vetores incompatíveis: o incremental aborta com erro
> `dims mismatch` nesse caso — rode uma vez com `--clean`, que recria a
> coleção automaticamente.
>
> Lotes de embed+upsert rodam em paralelo (`--embed-concurrency`, default 4)
> com retry (3 tentativas, backoff) — use `--embed-concurrency 1` para
> sequencial determinístico.

Além do hash de conteúdo, o indexer evita até **ler** um arquivo quando
mtime + tamanho + nº de chunks batem exatamente com o que já está indexado
— só nesse caso ele é pulado sem reabrir/rehashear; qualquer edição real
(ou uma reindexação anterior interrompida no meio) cai de volta no caminho
normal de releitura. Chunks cujo corpo já existe no índice (rename/move,
conteúdo deslocado ou duplicado entre arquivos) **copiam o vetor** em vez
de reembedar — rename custa milissegundos, não um reembed. Dentro de um
mesmo scan, arquivos modificados mais recentemente são processados primeiro.

### Modo contínuo (`--watch`)

```bash
# local:
go run ./cmd/indexer --all --watch --interval 10m
# docker (serviço dedicado, opt-in):
make docker-watch
# ou direto: docker compose --profile watch up -d indexer-watch
```

Repete a indexação incremental a cada `--interval` (default 10m; também
configurável no Docker via `REINDEX_INTERVAL`), até receber Ctrl-C/SIGTERM
— o progresso já persistido fica salvo, e a interrupção é reportada como
parada limpa, não como falha. `--watch` sempre roda em modo incremental
(ignora `--clean`): repetir um reindex completo a cada ciclo não
faria sentido. É esse ciclo periódico que também funciona como
"monitoramento" de exclusões: um arquivo apagado do disco tem seus pontos
removidos do Qdrant no ciclo seguinte, com defasagem máxima igual ao
intervalo configurado — não há verificação em tempo real (fsnotify).

Com `--generate-docs`, a etapa filedocs roda no 1º ciclo e depois no máximo
a cada `--docgen-interval` (default 24h ou `$DOCGEN_INTERVAL` — completions
são caras, docs não precisam do frescor do embedding), só para
`--docgen-projects` (csv, vazio = todos; `$DOCGEN_PROJECTS`). No Docker, a
mesma config vai via `DOCGEN_ENABLED=true` (liga sem a flag),
`DOCGEN_INTERVAL` e `DOCGEN_PROJECTS`:

```bash
# local: docs de 2 projetos, no máximo 1x ao dia, junto do watch de 10m:
go run ./cmd/indexer --all --watch --generate-docs \
  --docgen-projects neurolead,neurolead-client --docgen-interval 24h
```

## Modelos locais (Ollama — único modo)

Tudo roda local via Ollama, sem API externa e sem key: embedding com
`nomic-embed-text` (768 dims) e geração de docs com `qwen2.5-coder:7b`.
**Importante:** server e indexer precisam usar o mesmo modelo de embedding,
senão a busca não encontra nada (dims diferentes). O suporte a OpenAI foi
removido — se você ainda tem um índice antigo (`docs` com 1536 dims), rode
uma vez `indexer --all --clean` para reindexar em `docs` local e apague as
coleções legadas (`docs-ollama*`, se existirem).

> **GPU:** o Ollama do compose roda em CPU (~0,40s/lote-32 medido) contra
> ~0,13s no Ollama do host com GPU — 3x. Para indexar mais rápido, aponte
> `OLLAMA_URL` para o host (`http://host.docker.internal:11434` +
> `extra_hosts: ["host.docker.internal:host-gateway"]`) ou monte o compose
> com passthrough NVIDIA. O default (container CPU) é mais isolado e já
> sustenta ~260 textos/s com concorrência ≥2 (medido; `--embed-concurrency`
> acima de 4 não ajuda). Modelos ficam quentes por `OLLAMA_KEEP_ALIVE=24h`
> (default no compose) — sem isso, a primeira chamada após 5min ociosos paga
> reload de segundos (p50 0,01s vs picos de 2s medidos).

```bash
# 1. sobe tudo (qdrant + ollama + server):
docker compose up -d --build
# 2. baixa os modelos (uma vez):
docker compose exec ollama ollama pull nomic-embed-text   # embedding
docker compose exec ollama ollama pull qwen2.5-coder:7b   # docgen (ou: make ollama-pull)
# 3. indexa:
docker compose run --rm indexer --all
```

> Sem rede externa no Docker? Transfira os modelos do Ollama do host
> (offline, mesmo formato): `make ollama-seed`
> (`HOST_OLLAMA_MODELS=/caminho/do/host` se diferente do default).

Trocar de modelo Ollama (`OLLAMA_MODEL=mxbai-embed-large`) muda os dims
(1024) → exige reindex (`--clean`, coleção recriada automaticamente).

## Observabilidade (`/status` + `/dashboard`)

`GET /dashboard` é uma página viva (mesma origem, sem CDN) que lê
`GET /status` a cada 5s. O que cada nível mostra — tudo com dados reais,
nada de hipótese:

| Nível | Seção | Fonte real | Custo |
|---|---|---|---|
| 1 | Saúde da coleção (`status`, `optimizer_status`, `segments_count`) | Qdrant `GET /collections/{ativa}` | nenhum acesso novo |
| 1 | `indexed_vectors_count` vs `points_count` (aviso quando diff > 1000 ou > 5%) | mesmo `GET` acima | — |
| 1 | Coleções órfãs (ex.: índice legado de outra config) | `GET /collections` + count por coleção | — |
| 1 | `dims_mismatch` (modelo X dims da coleção — o bug documentado aqui vira aviso ativo) | compara `Embedder.Dims()` com `vectors.size` | — |
| 1 | Saúde do embedding (Ollama responde + tem o modelo?) | ping barato (`/api/tags`), sem gerar embedding (cache 30s) | — |
| 1 | Self do processo (uptime, goroutines, heap) + uso (`/mcp` contados via middleware, latência média) | `runtime` + contador atômico | — |
| 2 | Status do `indexer --watch` (último ciclo, duração, falhas, próximo ciclo + filedocs geradas/removidas por projeto) | heartbeat JSON que o indexer grava em `INDEXER_STATUS_FILE` (volume `status_state` compartilhado) | 1 volume a mais |
| 3 | Containers do projeto (estado de cada um) | Docker Engine API via socket Unix (`DOCKER_SOCKET`, só `GET` de leitura) | **privilegiado**: montar o socket dá ao container acesso à API do daemon — opt-in, descomente o volume no compose |
| 3 | Histórico (sparkline de pontos + requisições) | `GET /status/history?n=120` — 1 sample por `/status` real, em memória + JSONL (`STATUS_HISTORY_FILE`) | rotação embutida: acima de 5MB trunca pras últimas 20k linhas |

Sem o volume compartilhado, a seção do indexer some (não quebra o resto).
Sem o socket, a seção de containers some. Sem `STATUS_HISTORY_FILE`, o
histórico vive só em memória (720 samples, perde no restart).

Tuning do otimizador: toda coleção criada pelo indexer já nasce com
`deleted_threshold: 0.1` e `vacuum_min_vector_number: 500` (mais
agressivo que o default do Qdrant 0.2/1000 — validado ao vivo: 2.690 →
155 vetores pendentes). Coleções que já existiam precisam de um PATCH
único (o `EnsureCollection` não altera coleção existente de propósito):

```bash
curl -X PATCH localhost:6333/collections/docs \
  -H 'Content-Type: application/json' \
  -d '{"optimizers_config": {"deleted_threshold": 0.1, "vacuum_min_vector_number": 500}}'
```

## Segurança e operação

- **Auth HTTP (opt-in):** `MCP_AUTH_TOKEN` exige Bearer em `/mcp`, `/search`,
  `/status`, `/dashboard` (`/health` fica aberto p/ liveness). Sem token,
  o server avisa no boot e serve aberto — ok em localhost, não exponha
  `8080`/`6333` sem token.
- **API key do Qdrant (opt-in):** com `QDRANT_API_KEY` no serviço `qdrant`
  (descomente no compose), todos os clients precisam da mesma key
  (`QDRANT_API_KEY` nos demais serviços — já mandam o header). Ative com
  a stack parada e suba tudo junto, senão você mesmo se tranca pra fora.
- **Backup:** `make backup` gera `backups/qdrant-storage-<data>.tgz`
  (volume do Qdrant). Automatize via cron se o índice for crítico
  (reindex cheio leva ~10min; filedocs de projetos grandes, bem mais).
- **Versão do binário:** `make build` grava o git-sha em `index_status`
  (`1.0.0+a1b2c3d`) — detecta server desatualizado sem adivinhar.

## Testes

```bash
make test               # unitários, sem dependências externas (httptest fakes)
make test-integration   # contra um Qdrant real (precisa estar rodando)
make eval               # golden set de retrieval (recall@5) contra o server rodando
```

Os testes de integração (`internal/indexer/integration_test.go`) só rodam
com `RUN_QDRANT_INTEGRATION=1` — cobrem o ciclo incremental completo (diff
por hash/mtime, streaming com persistência parcial, self-heal de uma
atualização interrompida no meio) contra um Qdrant de verdade, em coleções
descartáveis que são apagadas ao final de cada teste.

## Estrutura

```
cmd/server      → binário MCP (stdio|http|both)
cmd/indexer     → CLI de indexação (--all, --watch, --generate-docs)
internal/chunker→ scan + chunking
internal/embed  → Ollama local (interface Provider)
internal/vector → cliente REST Qdrant
internal/indexer→ orquestra scan→embed→upsert
internal/mcpserver → tools MCP
internal/httpapi   → /health + /search debug
```

## Env vars

| Var | Default | Descrição |
|---|---|---|
| `EMBED_PROVIDER` | `ollama` | reservado (só `ollama`; `openai` foi removido e dá erro explícito) |
| `OLLAMA_URL` | `http://localhost:11434` | `http://ollama:11434` no compose |
| `OLLAMA_MODEL` | `nomic-embed-text` | modelo local de embedding (768 dims) |
| `OLLAMA_EMBED_DIMS` | `768` | 1024 p/ mxbai-embed-large |
| `QDRANT_URL` | `http://localhost:6333` | `http://qdrant:6333` no compose |
| `QDRANT_COLLECTION` | `docs` | coleção única (filedocs usa `docs-filedocs`) |
| `PROJECTS_ROOT` | `/projects` | raiz dos projetos |
| `HTTP_PORT` | `8080` | porta HTTP |
| `INDEXER_STATUS_FILE` | — | heartbeat do `--watch` (omitido se vazio) |
| `STATUS_HISTORY_FILE` | — | JSONL da série temporal (só memória se vazio) |
| `DOCKER_SOCKET` | — | socket Docker opt-in (omitido se vazio) |
| `COMPOSE_PROJECT_NAME` | — | filtra containers por projeto (vazio = todos) |
| `DOCGEN_PROVIDER` | `ollama` | reservado (só `ollama`) |
| `DOCGEN_MODEL` | `qwen2.5-coder:7b` | modelo local de geração |
| `DOCGEN_ENABLED` | — | `true` liga o `--generate-docs` do indexer sem a flag |
| `DOCGEN_INTERVAL` | `24h` | intervalo mínimo da etapa filedocs no `--watch` |
| `DOCGEN_PROJECTS` | — | csv de projetos com docs no `--watch` (vazio = todos) |
| `MCP_AUTH_TOKEN` | — | `Authorization: Bearer` exigido em /mcp, /search, /status, /dashboard (vazio = aberto; /health sempre aberto) |

## Documentação automática por arquivo (`cmd/docgen` + coleção `-filedocs`)

Além de indexar para busca, o projeto gera **documentação legível por
arquivo** (propósito, API pública, dependências, observações) via LLM local
(`qwen2.5-coder:7b`, validado: doc correta em PT, sem inventar). A doc é
persistida na coleção `<base>-filedocs` (1 ponto por arquivo, vetor =
embedding da prosa) e servida via `get_file_doc`/`search_file_docs`;
`list_docs` marca quem tem doc e `index_status`/`/status` reportam a cobertura.

```bash
# um pacote (stdout + Qdrant):
go run ./cmd/docgen --project demo --path ./internal/indexerstatus
# todos os projetos, salvando em docs/ (espelha <projeto>/<path>.md):
go run ./cmd/docgen --all --out ./docs-geradas
# só markdown, sem tocar no Qdrant:
go run ./cmd/docgen --all --store=false --out ./docs-geradas
# como etapa do indexer (após o embed+upsert de cada projeto):
go run ./cmd/indexer --all --generate-docs
# simular (sem LLM, sem writes):
go run ./cmd/docgen --all --dry-run
```

Regras da v1: mesmas exclusões do chunker (segredos, lockfiles, binários),
pula arquivos com <20 linhas e maiores que 30KB (`--max-bytes`), concorrência
2 (`--concurrency`), incremental por `source_hash` (só gera para
novos/alterados; remove pontos de arquivos apagados). Toda doc gerada
carrega o aviso "conteúdo de LLM, pode conter erros" — os chunks brutos
continuam a fonte autoritativa.

## Roadmap

- [x] Geração automática de documentação por arquivo — Fases 1-4 prontas:
  `internal/docgen` (interface + provider Ollama) + `internal/filedocs`
  (coleção `-filedocs`, incremental por `source_hash`, `--dry-run`) +
  `cmd/docgen` (CLI) + flag `--generate-docs` no `cmd/indexer` (com intervalo
  próprio `--docgen-interval` default 24h e opt-in por projeto
  `--docgen-projects` no `--watch`) + tools
  `get_file_doc`/`search_file_docs` + `has_doc` em `list_docs` + cobertura em
  `index_status`/`/status` (incl. seção no `/dashboard`).
- [x] Remoção OpenAI (só modelos locais) — `internal/embed/openai.go` e
  `internal/docgen/openai.go` deletados; `EMBED_PROVIDER` default `ollama`
  (`openai` dá erro explícito); coleção canônica `docs` (era `docs-ollama`);
  índice legado OpenAI (1536 dims) migrado via reindex `--clean`. Ver
  "Modelos locais" acima.
- [x] [Endurecimento do reprocessamento incremental](docs/plano-auto-documentacao.md#anexo-a--endurecimento-do-reprocessamento-incremental-pré-requisito-2026-10-07) —
  8 pontos implementados em 2026-10-07 (pré-requisito do docgen): default
  incremental (`Clean=false`), visibilidade `pulados sem ler` vs `hash igual`
  vs `copiados`, reuso por conteúdo sem reembed (rename/move/duplicata),
  índice payload + paginação do scroll, embed com pool+retry, abort em troca
  de dims, `--dry-run`.
