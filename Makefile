.PHONY: tidy build test test-integration run-http run-stdio index index-all docker-up docker-index docker-logs qdrant-ui ollama-pull ollama-seed docker-watch verify backup eval

VERSION := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
export VERSION
LDFLAGS := -X doc-rag-mcp/internal/config.buildVersion=$(VERSION)

tidy:
	go mod tidy

test:
	go test ./...

# Testes de integração (Qdrant real) rodam junto com `make test` quando há
# Qdrant acessível; este alvo força com coleções descartáveis próprias.
test-integration:
	RUN_QDRANT_INTEGRATION=1 QDRANT_URL=$${QDRANT_URL:-http://localhost:6333} go test ./internal/indexer/... -run Integration -v

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mcp-server ./cmd/server
	go build -ldflags "$(LDFLAGS)" -o bin/mcp-indexer ./cmd/indexer
	go build -ldflags "$(LDFLAGS)" -o bin/mcp-docgen ./cmd/docgen

# Copia os modelos do Ollama do host para o volume do compose (offline —
# o container pode não ter rede externa). Precisa do path do host
# (default: /usr/share/ollama/.ollama/models, usuário `ollama`).
ollama-seed:
	docker run --rm -v doc-rag-mcp_ollama_data:/dest -v $${HOST_OLLAMA_MODELS:-/usr/share/ollama/.ollama/models}:/src:ro alpine sh -c 'mkdir -p /dest/models && cp -rn /src/manifests /dest/models/manifests 2>/dev/null; cp -rn /src/blobs /dest/models/blobs 2>/dev/null; echo "modelos copiados — confira com: docker exec doc-rag-ollama ollama list"'

# Golden set de retrieval (recall@5): detecta regressão em troca de
# modelo/pesos. Precisa do server rodando (default localhost:8080).
eval:
	SERVER=$${SERVER:-http://localhost:8080} ./scripts/eval.sh

# Snapshot do volume do Qdrant (rollback de migrações). Guarda em
# ./backups/ com timestamp. Automatize via cron se o índice for crítico.
backup:
	mkdir -p backups
	docker run --rm -v doc-rag-mcp_qdrant_storage:/data -v ./backups:/backup alpine tar czf /backup/qdrant-storage-$$(date +%F-%H%M).tgz -C /data .
	@echo "backup em $$(ls -t backups/qdrant-storage-*.tgz | head -n 1)"

run-http: build
	QDRANT_URL=$${QDRANT_URL:-http://localhost:6333} ./bin/mcp-server --transport http --http-addr :$${HTTP_PORT:-8080}

run-stdio: build
	./bin/mcp-server --transport stdio

# Um projeto (ex.: make index PROJECT=neurolead PROJECT_PATH=/caminho).
# Sem PROJECT_PATH, o path é resolvido via $PROJECTS_ROOT/<project>.
index: build
	go run ./cmd/indexer --project $${PROJECT:-neurolead} $${PROJECT_PATH:+--path $${PROJECT_PATH}}

index-all: build
	go run ./cmd/indexer --all

# Baixa os modelos no Ollama (docker ou local): embedding + geração p/ docgen
ollama-pull:
	docker compose exec ollama ollama pull $${OLLAMA_MODEL:-nomic-embed-text}
	docker compose exec ollama ollama pull $${DOCGEN_MODEL:-qwen2.5-coder:7b}

docker-up:
	docker compose up -d --build

docker-index:
	docker compose build indexer
	docker compose run --rm indexer --all

# Indexador contínuo (incremental a cada REINDEX_INTERVAL, default 10m),
# com limpeza automática de pontos de arquivos excluídos no mesmo ciclo.
docker-watch:
	docker compose --profile watch up -d --build indexer-watch

docker-logs:
	docker compose logs -f mcp-server

qdrant-ui:
	@echo "Qdrant dashboard: http://localhost:6333/dashboard"

# Verificação ponta a ponta: sobe a stack (qdrant + ollama + mcp-server),
# espera a saúde, roda unitários + integração ao vivo + dry-run de indexação
# e mostra o /status. É o "está tudo de pé?" em um comando só. Sem nenhuma
# key externa (tudo local via Ollama).
verify: build
	docker compose up -d --build
	@echo "aguardando Qdrant..."
	@tries=0; while [ $$tries -lt 24 ]; do \
		curl -sf -m 2 http://localhost:6333/collections >/dev/null && break; \
		tries=$$((tries+1)); sleep 5; \
	done
	@curl -sf -m 2 http://localhost:6333/collections >/dev/null || (echo "Qdrant não subiu em 2min"; exit 1)
	@echo "aguardando mcp-server..."
	@tries=0; while [ $$tries -lt 24 ]; do \
		curl -sf -m 2 http://localhost:$${HTTP_PORT:-8080}/health >/dev/null && break; \
		tries=$$((tries+1)); sleep 5; \
	done
	@curl -sf http://localhost:$${HTTP_PORT:-8080}/health || (echo "mcp-server não respondeu /health em 2min"; exit 1)
	$(MAKE) test
	$(MAKE) test-integration
	# O `up` acima só (re)constrói qdrant+server; o indexer (profile tools)
	# precisa de build explícito, senão o `run` usa a imagem antiga.
	docker compose build indexer
	docker compose run --rm indexer --all --dry-run
	@echo "── GET /status ──"
	@curl -sf http://localhost:$${HTTP_PORT:-8080}/status | python3 -m json.tool | head -n 40
	@echo "verify ok: stack de pé, testes verdes, dry-run + /status conferidos"
