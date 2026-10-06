---
name: doc-rag-search
description: Use ao buscar código/documentação em projetos indexados pelo doc-rag-mcp — decide quando usar as tools MCP (search_docs, grep_docs, get_document, list_projects, list_docs, index_status) vs. busca tradicional de arquivos (grep/rg, find, ler arquivo), e como combinar as duas para respostas completas e corretas.
---

# Busca com doc-rag-mcp + busca tradicional de arquivos

Este skill ensina a usar bem as tools do **doc-rag-mcp** (RAG sobre
código/documentação de vários projetos, servido via MCP) em conjunto com as
ferramentas tradicionais de busca em arquivo (grep/ripgrep, find, leitura
direta). As duas fontes respondem perguntas diferentes — usar só uma delas
produz respostas incompletas ou erradas.

## Modelo mental (leia antes de buscar)

O índice RAG **não é um espelho fiel do disco**:

- **Incompleto por design** — regras de indexação excluem lockfiles,
  arquivos minificados, binários etc. (ver `## Como pesquisar bem` no
  `README.md` do projeto). Um grep local pode achar mais arquivos que o
  índice.
- **Pode estar desatualizado** — o índice só reflete o que o `indexer`
  processou até agora (full ou `--watch`/incremental, com defasagem até o
  `--interval` configurado). Edições muito recentes no checkout local podem
  não estar lá ainda. Use `index_status`/`list_projects` para checar
  freshness antes de confiar cegamente no conteúdo retornado.
- **Cross-project por padrão** — sem `project`, as tools buscam em todos os
  projetos indexados simultaneamente. Ótimo para "em qual desses repos isso
  existe?", ruim (ruidoso) quando você já sabe o projeto.
- **Ranqueado, não exaustivo** — `search_docs` devolve os trechos mais
  relevantes (semântica + keyword), não "todas as ocorrências". Para
  completude, a tool certa é `grep_docs`, não `search_docs` com `top_k` alto.
- **Trechos truncados** — `search_docs` corta conteúdo em ~1500 chars por
  trecho. Nunca edite/decida com base nesse trecho truncado: busque o
  arquivo completo antes (`get_document` ou leitura local).

## Quando usar o quê

| Preciso de... | Use |
|---|---|
| Entender como algo funciona / explicar um padrão em prosa | `search_docs` (semântico) |
| Lista exaustiva de todos os arquivos que mencionam um termo | `grep_docs` (MCP, cross-project) **e/ou** `rg`/`grep` local se o repo estiver no seu checkout |
| Conteúdo completo de um arquivo específico já identificado | Se o repo está no seu checkout local: **leitura direta do arquivo** (autoritativa, sempre atual). Se é um projeto que você não tem localmente (ou quer o que está indexado): `get_document` |
| Mapear a estrutura de um projeto | `list_docs` (indexado) + `find`/`ls` local quando disponível |
| Quais projetos existem no RAG, há quanto tempo foram indexados | `list_projects` |
| Saúde/estado do índice (provider, dims, nº de pontos) | `index_status` |
| Editar um arquivo | **Nunca edite a partir do conteúdo do RAG.** Releia o arquivo local antes de qualquer edição — o índice pode estar defasado em relação ao disco. |

## Fluxo recomendado

1. **Inventário primeiro, leitura depois.** Se a tarefa exige completude
   ("quantos lugares usam X", "onde X é chamado"), comece com `grep_docs`
   (cross-project, barato) antes de `search_docs`. `search_docs` entra para
   *entender* os grupos que o grep achou, não para descobri-los.
2. **Cruze com busca local quando o projeto estiver no seu checkout.** O
   índice pode ter um gap de arquivos excluídos ou não reindexados ainda.
   Quando a precisão importa (vai editar, vai afirmar algo categórico), rode
   também `rg -i "<termo>" <path>` local e compare a contagem de arquivos
   com o `grep_docs`. Divergência grande = índice desatualizado ou arquivos
   fora das regras de indexação — não é bug, é esperado; mencione isso ao
   reportar resultados.
3. **Escopo por projeto sempre que souber qual é.** Passe `project` (CSV
   para múltiplos) em `search_docs`/`grep_docs`/`list_docs` — reduz ruído e
   custo. Só deixe vazio quando a pergunta é "em qual projeto isso vive?".
4. **Nunca aja (editar, afirmar como fato final) sobre um trecho truncado.**
   Depois de achar o arquivo certo via `search_docs`/`grep_docs`, sempre
   busque o conteúdo completo — `get_document` (RAG) se não houver checkout
   local, ou a leitura de arquivo da sua ferramenta local se houver. A
   leitura local vence em caso de conflito: é o estado real atual.
5. **Verifique freshness quando o resultado for "estranho" ou crítico.**
   Se uma busca não acha algo que deveria existir, ou o conteúdo parece
   antigo, chame `index_status`/`list_projects` para ver a data da última
   indexação antes de concluir "não existe".
6. **MCP indisponível → caia 100% para ferramentas tradicionais.** Se o
   servidor MCP não estiver configurado/acessível, não bloqueie a tarefa:
   use grep/rg/find/leitura de arquivo normalmente e, se relevante, avise que
   a busca cross-project via RAG não estava disponível.

## Cheat sheet de parâmetros

- `search_docs(query, project?, top_k?, diversify?, max_per_file?, path_prefix?, language?, pure_vector?)`
  — `diversify=true` (padrão) limita a 2 trechos/arquivo para cobrir mais
  arquivos; desligue (`false`) só quando quiser o ranking puro de um arquivo
  específico. `pure_vector=true` desliga o rerank por keyword (útil para
  busca puramente conceitual, sem termo literal).
- `grep_docs(pattern, project?, path_prefix?, limit?)` — case-insensitive,
  literal, inventário completo (até `limit`, máx 500). Primeira tool para
  perguntas de "onde/quantos".
- `get_document(project, path)` — arquivo indexado completo, sem truncar.
  Use depois de localizar o arquivo certo.
- `list_projects()` — projetos + contagem de chunks + data da última
  indexação. Primeiro passo para checar freshness.
- `list_docs(project, limit?, prefix?)` — estrutura de arquivos indexados de
  um projeto.
- `index_status()` — provider/coleção/dims/total de pontos; serve para
  diagnosticar "por que a busca não encontra nada" (provider errado, coleção
  vazia, dims mismatch).

## Erros comuns a evitar

- Usar `search_docs` para contar/listar ocorrências (ele ranqueia, não
  enumera — gera números subestimados).
- Confiar em `search_docs`/`grep_docs` sem checar se o projeto em questão
  está indexado e atualizado (`list_projects`).
- Ignorar a busca local quando ela está disponível e é mais barata/precisa
  para o repo atual — o RAG brilha em busca **cross-project**, não substitui
  grep local dentro do próprio checkout.
- Editar código com base em `content` truncado de `search_docs`.
- Esquecer `project`/`path_prefix` em bases grandes, gerando ruído
  cross-project desnecessário.

## Como instalar este skill

Este arquivo é propositalmente portátil (markdown puro, sem sintaxe
específica de um client). Formas de uso:

- **Claude Code**: copie esta pasta para `.claude/skills/doc-rag-search/`
  (no projeto ou em `~/.claude/skills/` para disponibilizar globalmente).
- **Outros clients com MCP + "agent skills"** (Cursor, Opencode, etc.): cole
  o conteúdo deste arquivo nas regras/system prompt do projeto, ou aponte o
  client para esta pasta se ele suportar skills em markdown.
- **Uso manual / outro LLM qualquer**: cole o conteúdo da seção acima
  ("Modelo mental" até "Erros comuns") no system prompt sempre que o LLM
  tiver acesso ao MCP `doc-rag-mcp` (ver `examples/claude.json` e
  `examples/opencode.json` na raiz do repo para configurar a conexão).
