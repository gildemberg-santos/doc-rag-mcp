#!/bin/bash
# Golden set de retrieval: query|projeto|trecho-esperado-no-top5.
# Uso: SERVER=http://localhost:8080 ./scripts/eval.sh   (ou: make eval)
# Critério: recall@5 (path esperado entre os 5 primeiros). Serve para
# detectar regressão em troca de modelo/pesos/thresholds — rode antes e
# depois de qualquer mudança no ranking e compare.
# Conhecido (2026-10-13): "como funciona autenticação" retorna locale yml
# top-1 até em pure_vector (viés de idioma PT>code, margem 0.686 vs 0.683).
set -u
SERVER="${SERVER:-http://localhost:8080}"
PASS=0
FAIL=0

check() { # $1=query $2=projeto $3=trecho-esperado
  local q project want res
  q=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$1")
  project="$2"
  want="$3"
  res=$(curl -s -m 30 "$SERVER/search?q=$q&project=$project&k=5")
  if echo "$res" | python3 -c "
import json,sys
paths=[h['Path'] for h in json.load(sys.stdin)['results']]
sys.exit(0 if any('$want' in p for p in paths) else 1)"; then
    PASS=$((PASS+1))
    echo "HIT  [$1] ($2 ~ $3)"
  else
    FAIL=$((FAIL+1))
    top=$(echo "$res" | python3 -c "import json,sys; r=json.load(sys.stdin)['results']; print(r[0]['Path'] if r else '-')")
    echo "MISS [$1] (top: $top, esperava ~$3)"
  fi
  sleep 2
}

check "webhook" neurolead "webhook"
check "docker compose" doc-rag-mcp "docker-compose"
check "qwen modelo documentacao" doc-rag-mcp "plano-auto-documentacao"
check "sidekiq workers" atendimento "sidekiq"
check "como funciona autenticação" neurolead "session"

echo "── recall@5: $PASS/$((PASS+FAIL)) ──"
[ "$FAIL" -eq 0 ]
