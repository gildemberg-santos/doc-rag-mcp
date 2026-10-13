package clix

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("flag", "env", "cfg"); got != "flag" {
		t.Errorf("esperava flag, veio %q", got)
	}
	if got := FirstNonEmpty("", "env", "cfg"); got != "env" {
		t.Errorf("esperava env, veio %q", got)
	}
	if got := FirstNonEmpty("", "", "cfg"); got != "cfg" {
		t.Errorf("esperava cfg, veio %q", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Errorf("tudo vazio deveria dar vazio, veio %q", got)
	}
	if got := FirstNonEmpty("só-um"); got != "só-um" {
		t.Errorf("um valor deveria passar direto, veio %q", got)
	}
}

func TestOutDirLabel(t *testing.T) {
	if got := OutDirLabel(""); got != "stdout" {
		t.Errorf("out vazio deveria ser stdout, veio %q", got)
	}
	if got := OutDirLabel("/tmp/docs"); got != "/tmp/docs" {
		t.Errorf("out deveria passar direto, veio %q", got)
	}
}

func TestResolveTargetsSingleProject(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	got := ResolveTargets(root, false, "proj", "")
	if len(got) != 1 || got[0].Name != "proj" || got[0].Dir != dir {
		t.Fatalf("single-project deveria derivar path da root: %+v", got)
	}
}

func TestResolveTargetsAll(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, p), 0755); err != nil {
			t.Fatal(err)
		}
	}
	got := ResolveTargets(root, true, "", "")
	if len(got) != 2 {
		t.Fatalf("--all deveria listar as 2 pastas: %+v", got)
	}
}

func TestResolveTargetsProjectNameEnv(t *testing.T) {
	t.Setenv("PROJECT_NAME", "via-env")
	got := ResolveTargets(t.TempDir(), false, "", "")
	if len(got) != 1 || got[0].Name != "via-env" {
		t.Fatalf("PROJECT_NAME deveria nomear o projeto: %+v", got)
	}
}
