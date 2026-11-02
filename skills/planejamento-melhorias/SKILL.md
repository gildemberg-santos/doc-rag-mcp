---
name: planejamento-melhorias
description: Use para gerar, ponto a ponto, um planejamento de implementação detalhado e pronto para execução futura de cada ponto de melhoria levantado no projeto doc-rag-mcp (ou qualquer projeto de software).
---

# Planejamento de Implementação de Pontos de Melhoria

Este skill ensina a produzir um **plano de implementação detalhado** para cada ponto de melhoria levantado (retrospectiva, revisão de código, feedback de usuário, auditoria etc.), de forma que fique pronto para ser estimado e alocado em uma sprint/versão futura.

É especialmente útil quando há **vários pontos de melhoria** e cada um precisa de um documento próprio, estruturado e consistente, em vez de planos soltos e incompletos.

## Quando usar este skill

| Situação | Use este skill? |
|----------|-----------------|
| Levantou vários pontos de melhoria e quer planejar cada um antes de estimar | ✅ Sim |
| Precisa de um documento de planejamento padronizado para revisar com a equipe técnica | ✅ Sim |
| Quer garantir que cada ponto tenha escopo, etapas, testes, riscos e rollback definidos | ✅ Sim |
| Só quer anotar uma ideia rápida sem formalizar | ❌ Não — use uma issue ou checklist simples |

## Como usar (fluxo recomendado)

1. **Levante os pontos de melhoria** (ex: de uma retrospectiva, code review ou feedback de usuário).
2. **Para cada ponto**, preencha os placeholders do prompt abaixo com o contexto real do projeto e a descrição do ponto.
3. **Rode o prompt** em um LLM (ou peça para o agente aplicar este skill).
4. **Salve cada saída** como um arquivo separado, por exemplo: `planejamento-PM-001.md`, `planejamento-PM-002.md`, dentro de `docs/planejamentos/` ou similar.
5. **Revise com a equipe técnica** antes de atribuir esforço (horas/story points) ou alocar em sprint.
6. **Aprovação**: quem precisa aprovar fica definido na seção 10 do planejamento gerado.

---

## Prompt de instrução (copie e cole abaixo)

```
Você é um especialista em planejamento de software e gestão de projetos de tecnologia.

Seu objetivo é produzir um PLANO DE IMPLEMENTAÇÃO detalhado para um PONTO DE MELHORIA específico, destinado a ser executado em uma sprint/versão futura do projeto doc-rag-mcp.

CONTEXTO DO PROJETO:
{{CONTEXTO_DO_PROJETO}}

PONTO DE MELHORIA A SER PLANEJADO:
{{PONTO_DE_MELHORIA}}

INSTRUÇÕES:
Para o ponto de melhoria acima, crie um documento de planejamento contendo exatamente as seções listadas abaixo. Seja específico, realista e evite genéricos. Quando não houver informação suficiente, indique "[A DEFINIR]" e sugira o que precisa ser levantado.

1. IDENTIFICAÇÃO
   - Nome do ponto de melhoria
   - ID ou referência (ex: PM-001)
   - Descrição resumida do problema/oportunidade
   - Origem (ex: retrospectiva, revisão de código, feedback do usuário, auditoria)

2. OBJETIVO E ESCOPO
   - Objetivo principal da implementação
   - O que está DENTRO do escopo
   - O que está FORA do escopo (explicitamente excluído)
   - Critérios de sucesso (definição de pronto / Definition of Done)

3. ANÁLISE DE IMPACTO E DEPENDÊNCIAS
   - Módulos, arquivos ou componentes afetados (no doc-rag-mcp: cmd/, internal/, docs/, scripts/, etc.)
   - Dependências técnicas (bibliotecas Go, Ollama, Qdrant, APIs externas)
   - Dependências de outras pessoas/equipes
   - Impacto em usuários finais (quebrando mudança? retrabalho?)
   - Impacto em performance, segurança ou custo

4. ETAPAS DE IMPLEMENTAÇÃO
   Liste as etapas em ordem lógica, com:
   - Nome da etapa
   - Descrição do que será feito
   - Entregável de cada etapa
   - Esforço estimado (em horas ou story points)
   - Responsável sugerido (função, não nome)

5. PLANO DE TESTES E VALIDAÇÃO
   - Testes unitários a serem criados/atualizados (incl. testes do indexer, MCP server, etc.)
   - Testes de integração (contra Qdrant real quando aplicável)
   - Testes funcionais/E2E
   - Critérios de aceitação para cada etapa
   - Como validar que o problema foi resolvido

6. RISCOS E MITIGAÇÕES
   - Risco 1 + Mitigação 1
   - Risco 2 + Mitigação 2
   - ...

7. PLANO DE ROLLBACK / CONTINGÊNCIA
   - Como reverter a mudança se algo der erro em produção
   - Sinal de alerta para acionar o rollback
   - Tempo estimado para rollback

8. RECURSOS NECESSÁRIOS
   - Pessoas (perfis e quantidade)
   - Ferramentas/ambientes (Docker, Qdrant, Ollama, Go toolchain)
   - Acessos ou permissões especiais

9. CRONOGRAMA ESTIMADO
   - Data de início sugerida
   - Data de entrega sugerida
   - Marcos principais com datas

10. APROVAÇÕES E COMUNICAÇÃO
    - Quem precisa aprovar a implementação
    - Quem será comunicado ao iniciar e ao concluir
    - Canal de comunicação

FORMATO DE SAÍDA:
Retorne o planejamento em Markdown, usando tabelas onde fizer sentido, e mantenha uma linguagem clara e direta. Ao final, inclua um resumo executivo de 3 a 5 linhas.
```

---

## Exemplo de preenchimento (referência)

| Campo | Exemplo para este projeto |
|-------|---------------------------|
| `{{CONTEXTO_DO_PROJETO}}` | Servidor MCP de documentação com RAG (Go + Qdrant + Ollama, 100% local). Indexa repositórios, fatia em chunks, gera embeddings com Ollama (nomic-embed-text, 768 dims) e salva no Qdrant. Expõe busca semântica via tools MCP (search_docs, grep_docs, get_document, list_*). Time pequeno, stack Go. |
| `{{PONTO_DE_MELHORIA}}` | Reduzir o tempo de indexação incremental de projetos grandes (>10k arquivos), que atualmente leva mais de 10 minutos porque rehashia chunks inalterados. |

---

## Onde salvar os resultados

No repositório doc-rag-mcp, sugira salvar cada planejamento gerado em:

```
docs/planejamentos/planejamento-PM-001.md
docs/planejamentos/planejamento-PM-002.md
...
```

Mantenha um índice resumido em `docs/planejamentos/README.md` listando ID, nome, status (planejado / em implementação / concluído) e link para cada arquivo.

---

## Como instalar este skill

Este arquivo é propositalmente portátil (markdown puro, sem sintaxe específica de um client). Formas de uso:

- **No próprio projeto doc-rag-mcp**: este arquivo já está em `skills/planejamento-melhorias/SKILL.md`. Qualquer agente com acesso à pasta `skills/` pode lê-lo e aplicar as instruções.
- **Outros clients com suporte a skills locais** (Opencode, Claude Code, Cursor, etc.): copie esta pasta para a pasta de skills do client (ex: `.claude/skills/planejamento-melhorias/` ou `~/.claude/skills/planejamento-melhorias/` para uso global).
- **Uso manual / outro LLM qualquer**: cole o conteúdo da seção "Prompt de instrução" no system prompt sempre que precisar gerar um planejamento de ponto de melhoria, preenchendo os placeholders com os dados reais.

---

Dica: se quiser, posso gerar também um **modelo de arquivo Markdown vazio (checklist)** para preenchimento manual de cada ponto de melhoria, sem depender de IA.