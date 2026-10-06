.PHONY: tidy build test test-integration run-http run-stdio index index-all docker-up docker-index docker-logs qdrant-ui ollama-pull docker-index-ollama docker-watch

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

run-http: build
	QDRANT_URL=$${QDRANT_URL:-http://localhost:6333} ./bin/mcp-server --transport http --http-addr :$${HTTP_PORT:-8080}

run-stdio: build
	./bin/mcp-server --transport stdio

index: build
	go run ./cmd/indexer --project $${PROJECT:-neurolead} --path $${PATH project}

index-all: build
	go run ./cmd/indexer --all

# Indexação 100% local (coleção docs-ollama, não toca no índice OpenAI)
index-all-ollama: build
	go run ./cmd/indexer --all --provider ollama

# Baixa o modelo de embedding no Ollama (docker ou local)
ollama-pull:
	docker compose exec ollama ollama pull $${OLLAMA_MODEL:-nomic-embed-text}

docker-up:
	docker compose up -d --build

# Stack com Ollama local (sobe qdrant + ollama + server)
docker-up-ollama:
	docker compose --profile ollama up -d --build

docker-index:
	docker compose run --rm indexer --all

docker-index-ollama:
	docker compose run --rm -e EMBED_PROVIDER=ollama indexer --all --provider ollama

# Indexador contínuo (incremental a cada REINDEX_INTERVAL, default 10m),
# com limpeza automática de pontos de arquivos excluídos no mesmo ciclo.
docker-watch:
	docker compose --profile watch up -d --build indexer-watch

docker-logs:
	docker compose logs -f mcp-server

qdrant-ui:
	@echo "Qdrant dashboard: http://localhost:6333/dashboard"
