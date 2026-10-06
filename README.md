# doc-rag-mcp — MCP de Documentação com RAG (Go + Qdrant + OpenAI/Ollama)

Servidor **MCP** que expõe a documentação/código dos seus projetos via **RAG**:
escaneia os repositórios → fatia em chunks → gera embeddings (**OpenAI** ou
**Ollama local**) → salva no **Qdrant** (local, via Docker) → serve busca
semântica como **tools MCP** para qualquer LLM (Claude Code, Opencode, Cursor,
OpenAI, etc).

## Arquitetura

```
┌──────────────┐   indexa    ┌──────────┐  embeddings  ┌───────────┐
│  neurolead   │ ──────────▶ │ indexer  │ ───────────▶ │  Qdrant   │
│  neurolead-  │   chunks    │ (Go CLI) │  OpenAI API │ (Docker,  │
│  client      │             │          │  text-emb-  │  :6333)   │
└──────────────┘             └──────────┘  3-small    └─────┬─────┘
                                                              │ busca
┌────────────┐  MCP tools   ┌───────────────┐  query vec     │
│ Claude     │ ◀─────────── │  mcp-server   │ ─────────────┘
│ Opencode   │  stdio ou    │  (Go, tools:  │
│ OpenAI...  │  Streamable  │  search_docs, │
└────────────┘  HTTP /mcp   │  get_document,│
                            │  list_*, ...) │
                            └───────────────┘
```

**Tools MCP expostas:**

| Tool | Descrição |
|---|---|
| `search_docs` | Busca semântica + keyword (`query`, `project?` csv cross-project, `top_k?` máx 50, `diversify?`, `max_per_file?`, `path_prefix?`, `language?`, `pure_vector?`) |
| `grep_docs` | Inventário literal: todos os arquivos com o termo + ocorrências (`pattern`, `project?`, `path_prefix?`, `limit?` máx 500) |
| `get_document` | Arquivo completo (`project`, `path`) |
| `list_projects` | Projetos + contagem + data da última indexação |
| `list_docs` | Arquivos de um projeto (`project`, `limit?`, `prefix?`) |
| `index_status` | Provider, coleção, dims, total de pontos |

**Como pesquisar bem (lições da análise MCP-vs-grep):**
- `search_docs` = entender o padrão (ranking com diversidade por arquivo + match literal, força forte ≥0.55)
- `grep_docs` = inventário completo (ex.: 312 arquivos com "webhook" no índice vs 354 no `grep -ri` — o gap são arquivos fora das regras de indexação)
- Combine: `grep_docs` para mapear, `search_docs`/`get_document` para entender cada grupo

## Quickstart (Docker — recomendado)

```bash
cd doc-rag-mcp
cp .env.example .env   # preencha OPENAI_API_KEY
docker compose up -d --build     # sobe qdrant + mcp-server
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
QDRANT_URL=http://localhost:6333 OPENAI_API_KEY=sk-... ~/.local/bin/doc-rag-mcp --transport stdio
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
        "OPENAI_API_KEY": "sk-..."
      }
    }
  }
}
```

Veja `examples/opencode.json` e `examples/claude.json`.

### OpenAI / qualquer cliente Streamable HTTP

Endpoint: `POST http://localhost:8080/mcp` (MCP Streamable HTTP).
Debug REST (sem MCP): `GET /search?q=...&project=...`.

## Reindexar

```bash
# tudo (OpenAI → coleção docs):
docker compose run --rm indexer --all
# um projeto:
docker compose run --rm indexer --project neurolead --path /projects/neurolead
# incremental (mantém o existente):
docker compose run --rm indexer --all --no-clean
```

> O modo incremental (`--no-clean`) compara o hash do conteúdo de cada chunk
> com o que já está no Qdrant: chunks inalterados são pulados (sem chamar o
> provider de embedding), só os novos/alterados são reembedados, e pontos de
> arquivos removidos/renomeados/encurtados são apagados automaticamente.
> Na primeira execução incremental após atualizar para essa versão, todo o
> índice é reembedado uma vez (os pontos antigos não têm o novo esquema de
> ID/hash) — isso também limpa qualquer duplicação deixada por versões
> anteriores do modo incremental.

Além do hash de conteúdo, o indexer evita até **ler** um arquivo quando
mtime + tamanho + nº de chunks batem exatamente com o que já está indexado
— só nesse caso ele é pulado sem reabrir/rehashear; qualquer edição real
(ou uma reindexação anterior interrompida no meio) cai de volta no caminho
normal de releitura. Dentro de um mesmo scan, arquivos modificados mais
recentemente são processados primeiro.

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
(ignora `--no-clean=false`): repetir um reindex completo a cada ciclo não
faria sentido. É esse ciclo periódico que também funciona como
"monitoramento" de exclusões: um arquivo apagado do disco tem seus pontos
removidos do Qdrant no ciclo seguinte, com defasagem máxima igual ao
intervalo configurado — não há verificação em tempo real (fsnotify).

## Modo híbrido: Ollama local (grátis, offline)

O server e o indexer aceitam `--provider ollama` (ou `EMBED_PROVIDER=ollama`).
O índice OpenAI (`docs`, 1536 dims) fica **intacto** — o Ollama usa coleção
separada (`docs-ollama`, 768 dims). **Importante:** server e indexer precisam
usar o mesmo provider, senão a busca não encontra nada.

```bash
# 1. sobe ollama junto:
docker compose --profile ollama up -d --build
# 2. baixa o modelo (uma vez):
docker compose exec ollama ollama pull nomic-embed-text   # ou: make ollama-pull
# 3. indexa no modo local:
docker compose run --rm -e EMBED_PROVIDER=ollama indexer --all --provider ollama
# 4. serve no modo local:
EMBED_PROVIDER=ollama docker compose up -d mcp-server
# ou local: go run ./cmd/server --provider ollama
```

Trocar de modelo Ollama (`OLLAMA_MODEL=mxbai-embed-large`) muda os dims
(1024) → exige reindex (coleção recriada automaticamente).

## Observabilidade (`/status` + `/dashboard`)

`GET /dashboard` é uma página viva (mesma origem, sem CDN) que lê
`GET /status` a cada 5s. O que cada nível mostra — tudo com dados reais,
nada de hipótese:

| Nível | Seção | Fonte real | Custo |
|---|---|---|---|
| 1 | Saúde da coleção (`status`, `optimizer_status`, `segments_count`) | Qdrant `GET /collections/{ativa}` | nenhum acesso novo |
| 1 | `indexed_vectors_count` vs `points_count` (aviso quando diff > 1000 ou > 5%) | mesmo `GET` acima | — |
| 1 | Coleções órfãs (ex.: `docs` OpenAI parada enquanto `EMBED_PROVIDER=ollama`) | `GET /collections` + count por coleção | — |
| 1 | `dims_mismatch` (provider X dims da coleção — o bug documentado aqui vira aviso ativo) | compara `Embedder.Dims()` com `vectors.size` | — |
| 1 | Saúde do embedding (Ollama responde + tem o modelo? OpenAI key válida?) | ping barato, sem gerar embedding (cache 30s) | — |
| 1 | Self do processo (uptime, goroutines, heap) + uso (`/mcp` contados via middleware, latência média) | `runtime` + contador atômico | — |
| 2 | Status do `indexer --watch` (último ciclo, duração, falhas, próximo ciclo) | heartbeat JSON que o indexer grava em `INDEXER_STATUS_FILE` (volume `status_state` compartilhado) | 1 volume a mais |
| 3 | Containers do projeto (estado de cada um) | Docker Engine API via socket Unix (`DOCKER_SOCKET`, só `GET` de leitura) | **privilegiado**: montar o socket dá ao container acesso à API do daemon — opt-in, descomente o volume no compose |
| 3 | Histórico (sparkline de pontos + requisições) | `GET /status/history?n=120` — 1 sample por `/status` real, em memória + JSONL (`STATUS_HISTORY_FILE`) | 1 arquivo que cresce (~1 linha por `/status` real; `tail -n` + rotação externa resolvem) |

Sem o volume compartilhado, a seção do indexer some (não quebra o resto).
Sem o socket, a seção de containers some. Sem `STATUS_HISTORY_FILE`, o
histórico vive só em memória (720 samples, perde no restart).

Tuning do otimizador: toda coleção criada pelo indexer já nasce com
`deleted_threshold: 0.1` e `vacuum_min_vector_number: 500` (mais
agressivo que o default do Qdrant 0.2/1000 — validado ao vivo: 2.690 →
155 vetores pendentes). Coleções que já existiam precisam de um PATCH
único (o `EnsureCollection` não altera coleção existente de propósito):

```bash
curl -X PATCH localhost:6333/collections/docs-ollama \
  -H 'Content-Type: application/json' \
  -d '{"optimizers_config": {"deleted_threshold": 0.1, "vacuum_min_vector_number": 500}}'
```

## Testes

```bash
make test               # unitários, sem dependências externas (httptest fakes)
make test-integration   # contra um Qdrant real (precisa estar rodando)
```

Os testes de integração (`internal/indexer/integration_test.go`) só rodam
com `RUN_QDRANT_INTEGRATION=1` — cobrem o ciclo incremental completo (diff
por hash/mtime, streaming com persistência parcial, self-heal de uma
atualização interrompida no meio) contra um Qdrant de verdade, em coleções
descartáveis que são apagadas ao final de cada teste.

## Estrutura

```
cmd/server      → binário MCP (stdio|http|both, --provider openai|ollama)
cmd/indexer     → CLI de indexação (--all, --provider)
internal/chunker→ scan + chunking
internal/embed  → providers OpenAI + Ollama (interface Provider)
internal/vector → cliente REST Qdrant
internal/indexer→ orquestra scan→embed→upsert
internal/mcpserver → tools MCP
internal/httpapi   → /health + /search debug
```

## Env vars

| Var | Default | Descrição |
|---|---|---|
| `EMBED_PROVIDER` | `openai` | `openai` ou `ollama` (flag `--provider` sobrepõe) |
| `OPENAI_API_KEY` | — | obrigatória só no modo openai |
| `OPENAI_EMBED_MODEL` | `text-embedding-3-small` | modelo de embedding |
| `OPENAI_EMBED_DIMS` | `1536` | dims do vetor (3072 p/ large) |
| `OLLAMA_URL` | `http://localhost:11434` | `http://ollama:11434` no compose |
| `OLLAMA_MODEL` | `nomic-embed-text` | modelo local (768 dims) |
| `OLLAMA_EMBED_DIMS` | `768` | 1024 p/ mxbai-embed-large |
| `QDRANT_URL` | `http://localhost:6333` | `http://qdrant:6333` no compose |
| `QDRANT_COLLECTION` | `docs` | base; ollama usa `docs-ollama` |
| `PROJECTS_ROOT` | `/projects` | raiz dos projetos |
| `HTTP_PORT` | `8080` | porta HTTP |
| `INDEXER_STATUS_FILE` | — | heartbeat do `--watch` (omitido se vazio) |
| `STATUS_HISTORY_FILE` | — | JSONL da série temporal (só memória se vazio) |
| `DOCKER_SOCKET` | — | socket Docker opt-in (omitido se vazio) |
| `COMPOSE_PROJECT_NAME` | — | filtra containers por projeto (vazio = todos) |
