# Prompt — Planejamento de Implementação de Pontos de Melhoria

Use este prompt com um modelo de linguagem para gerar, para **cada ponto de melhoria identificado**, um plano de implementação detalhado e pronto para execução futura.

---

## Texto de instrução (copie e cole abaixo)

```
Você é um especialista em planejamento de software e gestão de projetos de tecnologia.

Seu objetivo é produzir um PLANO DE IMPLEMENTAÇÃO detalhado para um PONTO DE MELHORIA específico, destinado a ser executado em uma sprint/versão futura.

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
   - Módulos, arquivos ou componentes afetados
   - Dependências técnicas (bibliotecas, APIs, serviços externos)
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
   - Testes unitários a serem criados/atualizados
   - Testes de integração
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
   - Ferramentas/ambientes
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

## Como usar

1. Preencha os placeholders `{{CONTEXTO_DO_PROJETO}}` e `{{PONTO_DE_MELHORIA}}` com as informações reais de cada item.
2. Rode o prompt para cada ponto de melhoria levantado.
3. Salve cada saída como um arquivo separado (ex: `planejamento-PM-001.md`) na pasta de documentação do projeto.
4. Revise com a equipe técnica antes de estimar esforço ou alocar em sprint.

---

## Exemplo de preenchimento (referência)

| Campo | Exemplo |
|-------|---------|
| `{{CONTEXTO_DO_PROJETO}}` | Aplicação web de gestão de documentos, stack React + Node.js + PostgreSQL, hospedada na AWS, time de 3 desenvolvedores. |
| `{{PONTO_DE_MELHORIA}}` | Reduzir o tempo de upload de arquivos grandes (>100 MB) que atualmente trava a interface por mais de 30 segundos. |

---

Dica: se quiser, posso gerar também um **modelo de arquivo Markdown vazio** (checklist) para você preencher manualmente cada ponto de melhoria, sem depender de IA.