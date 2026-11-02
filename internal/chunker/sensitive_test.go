package chunker

import "testing"

func TestIsSensitiveContent(t *testing.T) {
	tests := []struct {
		name  string
		content string
		want  bool
	}{
		{
			name:     "Stripe test key",
			content:  "sk_test_51ABCDEFGHIJKLMNOPQRSTUVWXYZ",
			want:     true,
		},
		{
			name:     "AWS access key",
			content:  "AKIAIOSFODNN7EXAMPLE",
			want:     true,
		},
		{
			name:     "password assignment",
			content:  "password = \"minhasenha123\"",
			want:     true,
		},
		{
			name:     "api_key assignment",
			content:  "api_key = \"abc123def456ghi789jkl\"",
			want:     true,
		},
		{
			name:     "postgres connection string with password",
			content:  "postgres://admin:secret@db.example.com:5432/mydb",
			want:     true,
		},
		{
			name:     "private key PEM",
			content:  "-----BEGIN RSA PRIVATE KEY-----\nMIIEogIBAAKCAQEA...",
			want:     true,
		},
		{
			name:     "JWT token",
			content:  "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNtPnEEz12345",
			want:     true,
		},
		{
			name:     "GitHub token",
			content:  "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCDE",
			want:     true,
		},
		{
			name:     "CPF-like number",
			content:  "123.456.789-09",
			want:     true,
		},
		{
			name:     "CNPJ-like number",
			content:  "12.345.678/0001-90",
			want:     true,
		},
		{
			name:     "legitimate code mentioning password in comment",
			content:  "// não armazene senha aqui, use variável de ambiente",
			want:     false,
		},
		{
			name:     "code without secrets",
			content:  "func hello() { println(\"ola mundo\") }",
			want:     false,
		},
		{
			name:     "empty content",
			content:  "",
			want:     false,
		},
		{
			name:     "variable named senha but no value pattern",
			content:  "var senha string",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSensitiveContent(tt.content); got != tt.want {
				t.Errorf("isSensitiveContent(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
