package chunker

import (
	"os"
	"path/filepath"
	"testing"
)

func statFor(t *testing.T, size int) os.FileInfo {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, make([]byte, size), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// Casos encontrados numa auditoria real contra projetos Rails (config de
// segredos com extensão permitida, pastas de build sob public/, código
// legítimo cujo nome só cita "credential"/"secret" como conceito de domínio).
func TestShouldIndexRejectsSensitiveConfig(t *testing.T) {
	info := statFor(t, 10)
	rejected := []string{
		"config/secrets.yml",
		"config/database.yml",
		"config/newrelic.yml",
		"config/scout_apm.yml",
		"config/elastic_apm.yml",
		"config/my_app_secrets.json",
		"api_credentials.yml",
	}
	for _, rel := range rejected {
		if shouldIndex(rel, info) {
			t.Errorf("shouldIndex(%q) = true, esperava false (config sensível)", rel)
		}
	}
}

func TestShouldIndexAllowsExampleTemplates(t *testing.T) {
	info := statFor(t, 10)
	allowed := []string{
		"config/credentials/credentials.example.yml",
		"config/database.sample.yml",
		".env.example",
	}
	for _, rel := range allowed {
		if !shouldIndex(rel, info) {
			t.Errorf("shouldIndex(%q) = false, esperava true (é um template/exemplo, não segredo real)", rel)
		}
	}
}

func TestShouldIndexAllowsLegitimateSourceAboutCredentials(t *testing.T) {
	info := statFor(t, 10)
	allowed := []string{
		"app/models/marketing_event_credential.rb",
		"app/services/ecommerces/integrations/vtex/api/credentials/api_key.rb",
		"app/javascript/components/CredentialsTable.jsx",
	}
	for _, rel := range allowed {
		if !shouldIndex(rel, info) {
			t.Errorf("shouldIndex(%q) = false, esperava true (código-fonte legítimo, não config de segredo)", rel)
		}
	}
}

func TestShouldIndexRejectsGeneratedPublicDirs(t *testing.T) {
	info := statFor(t, 10)
	rejected := []string{
		"public/assets/application-ab12cd34.js",
		"public/packs/js/app-ab12cd34.js",
		"public/packs-test/manifest.json",
		"public/vite-dev/main.js",
	}
	for _, rel := range rejected {
		if shouldIndex(rel, info) {
			t.Errorf("shouldIndex(%q) = true, esperava false (saída de build sob public/)", rel)
		}
	}
}

func TestShouldIndexAllowsAppAssetsSource(t *testing.T) {
	info := statFor(t, 10)
	// app/assets É código-fonte (Sass/JS escrito à mão) — só public/assets
	// é saída de build; não devem ser confundidos.
	allowed := []string{
		"app/assets/stylesheets/application.scss",
		"app/assets/javascripts/app.js",
	}
	for _, rel := range allowed {
		if !shouldIndex(rel, info) {
			t.Errorf("shouldIndex(%q) = false, esperava true (código-fonte em app/assets, não build em public/assets)", rel)
		}
	}
}
