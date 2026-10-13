.PHONY: tidy build test test-integration run-http run-stdio index index-all docker-up docker-index docker-logs qdrant-ui ollama-pull docker-watch verify

tidy:
	go mod tidy

test:
	go test ./...

# Testes de integração contra um Qdrant real (precisa estar rodando, ex.:
# docker compose up -d qdrant). Não entram no `make test`/`go test ./...`
# normal — usam coleções descartáveis próprias, limpas ao final.
test-integration:
	RUN_QDRANT_INTEGRATION=1 QDRANT_URL=$${QDRANT_URL:-http://localhost:6333} go test ./internal/indexer/... -run Integration -v

build:
	go build -o bin/mcp-server ./cmd/server
	go build -o bin/mcp-indexer ./cmd/indexer
	go build -o bin/mcp-docgen ./cmd/docgen

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
