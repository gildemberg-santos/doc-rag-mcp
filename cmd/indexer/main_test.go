package main

import (
	"testing"
	"time"
)

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"flag", "env", "flag"},
		{"", "env", "env"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := firstNonEmpty(c.a, c.b); got != c.want {
			t.Errorf("firstNonEmpty(%q, %q) = %q, esperava %q", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveCleanDefaultsToIncremental(t *testing.T) {
	// Sem flags: incremental (o novo default — nunca apaga tudo antes).
	if got := resolveClean(false, true, false); got {
		t.Error("sem flags deveria ser incremental (false), veio reindex (true)")
	}
}

func TestResolveCleanFlagForcesReindex(t *testing.T) {
	if got := resolveClean(true, true, false); !got {
		t.Error("--clean deveria forçar reindex (true)")
	}
}

func TestResolveCleanDeprecatedAliasStillWorks(t *testing.T) {
	// Compat: quem passava --no-clean=false explicitamente continua
	// conseguindo o reindex completo de antes.
	if got := resolveClean(false, false, false); !got {
		t.Error("--no-clean=false deveria equivaler a --clean (true)")
	}
	// E --no-clean (default true) não muda nada sozinho.
	if got := resolveClean(false, true, false); got {
		t.Error("--no-clean (default) sozinho deveria manter incremental (false)")
	}
}

func TestResolveCleanWatchAlwaysIncremental(t *testing.T) {
	// --watch ignora qualquer pedido de reindex: repetir um full a cada
	// ciclo não faria sentido.
	if got := resolveClean(true, true, true); got {
		t.Error("--watch + --clean deveria continuar incremental (false)")
	}
	if got := resolveClean(false, false, true); got {
		t.Error("--watch + --no-clean=false deveria continuar incremental (false)")
	}
}

func TestParseDocgenProjects(t *testing.T) {
	if got := parseDocgenProjects(""); len(got) != 0 {
		t.Errorf("csv vazio deveria dar set vazio (todos), veio %v", got)
	}
	got := parseDocgenProjects(" neurolead, neurolead-client ,,")
	if len(got) != 2 || !got["neurolead"] || !got["neurolead-client"] {
		t.Errorf("csv deveria virar set com 2 projetos (trim + sem vazios), veio %v", got)
	}
}

func TestDocgenDue(t *testing.T) {
	now := time.Now()
	// Primeiro ciclo (last zero) sempre roda.
	if !docgenDue(time.Time{}, now, 24*time.Hour) {
		t.Error("primeiro ciclo deveria rodar filedocs")
	}
	last := now.Add(-25 * time.Hour)
	if !docgenDue(last, now, 24*time.Hour) {
		t.Error("após o intervalo deveria rodar filedocs")
	}
	if docgenDue(now.Add(-time.Hour), now, 24*time.Hour) {
		t.Error("dentro do intervalo deveria adiar filedocs (throttle)")
	}
	// Intervalo <=0 desliga o throttle.
	if !docgenDue(now.Add(-time.Minute), now, 0) {
		t.Error("intervalo<=0 deveria rodar todo ciclo")
	}
}

func TestDocgenIntervalDefault(t *testing.T) {
	t.Setenv("DOCGEN_INTERVAL", "")
	if got := docgenIntervalDefault(); got != 24*time.Hour {
		t.Errorf("sem env deveria ser 24h, veio %s", got)
	}
	t.Setenv("DOCGEN_INTERVAL", "12h")
	if got := docgenIntervalDefault(); got != 12*time.Hour {
		t.Errorf("DOCGEN_INTERVAL=12h deveria valer, veio %s", got)
	}
	t.Setenv("DOCGEN_INTERVAL", "lixo")
	if got := docgenIntervalDefault(); got != 24*time.Hour {
		t.Errorf("env inválido deveria cair no default 24h, veio %s", got)
	}
}
