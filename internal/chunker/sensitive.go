// Package chunker (extensão): detecção de segredos e PII por conteúdo.
//
// Este arquivo é parte de `internal/chunker` e adiciona a capacidade de
// identificar padrões de credenciais e dados pessoais no conteúdo dos
// arquivos, independentemente do nome do arquivo. Complementa as regras
// existentes de exclusão por nome (`sensitiveBaseNames`, `configLikeExts`).
//
// Não é exaustivo: cobre os padrões mais comuns de vazamento acidental.
// Não substitui boas práticas de secrets management (ex.: variáveis de
// ambiente, cofres de segredos).
package chunker

import "regexp"

// sensitivePatterns detecta padrões de segredos e PII no conteúdo dos arquivos.
// Cada padrão é compilado uma vez no init do pacote.
var sensitivePatterns = []*regexp.Regexp{
	// Chaves de API Stripe
	regexp.MustCompile(`(?i)sk_(live|test)_[A-Za-z0-9]{24,}`),
	// AWS Access Key ID
	regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16}`),
	// Senhas em atribuição (password = "...", passwd: "...", etc.)
	regexp.MustCompile(`(?i)(?:password|passwd|pwd)\s*[:=]\s*['\"]?[^ \s'\"]{8,}`),
	// API keys genéricas (api_key = "...", apikey: "...")
	regexp.MustCompile(`(?i)(?:api[_-]?key|apikey)\s*[:=]\s*['"]?[A-Za-z0-9_-]{20,}`),
	// Strings de conexão de banco com senha embutida
	regexp.MustCompile(`(?i)postgres://[^:]+:[^@]+@`),
	regexp.MustCompile(`(?i)mysql://[^:]+:[^@]+@`),
	regexp.MustCompile(`(?i)mongodb://[^:]+:[^@]+@`),
	// Chaves privadas PEM
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]+PRIVATE KEY-----`),
	// CPF brasileiro (formato simplificado, pode gerar falso positivo em
	// números longos genéricos; tolerável por ser detecção de conteúdo)
	regexp.MustCompile(`\b\d{3}\.?\d{3}\.?\d{3}[-]?\d{2}\b`),
	// CNPJ brasileiro (formato simplificado)
	regexp.MustCompile(`\b\d{2}\.?\d{3}\.?\d{3}\/?\d{4}\-?\d{2}\b`),
	// Tokens JWT (três blocos base64 separados por ponto)
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	// GitHub tokens clássicos / fine-grained
	regexp.MustCompile(`(?i)(ghp_|github_pat_|gho_|ghu_|ghs_|ghr_)[A-Za-z0-9]{36,}`),
}

// isSensitiveContent verifica se o conteúdo contém padrões de segredos/PII.
// Retorna verdadeiro se qualquer padrão bater. Não exibe o trecho encontrado
// nos logs para evitar gravar o segredo acidentalmente.
func isSensitiveContent(content string) bool {
	for _, re := range sensitivePatterns {
		if re.MatchString(content) {
			return true
		}
	}
	return false
}
