package indexer

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestPointIDIsDeterministic(t *testing.T) {
	a := pointID("proj", "app/models/user.rb", 0)
	b := pointID("proj", "app/models/user.rb", 0)
	if a != b {
		t.Fatalf("pointID deveria ser determinístico: %q != %q", a, b)
	}
}

func TestPointIDDiffersByInput(t *testing.T) {
	base := pointID("proj", "a.rb", 0)
	cases := map[string]string{
		"projeto diferente":     pointID("outro-proj", "a.rb", 0),
		"path diferente":        pointID("proj", "b.rb", 0),
		"chunk_index diferente": pointID("proj", "a.rb", 1),
	}
	for name, id := range cases {
		if id == base {
			t.Errorf("%s: esperava ID diferente do base, veio igual (%q) — colisão quebraria dedup/upsert", name, id)
		}
	}
}

func TestPointIDLooksLikeUUID(t *testing.T) {
	id := pointID("proj", "a.rb", 0)
	// formato 8-4-4-4-12 hex com hífens, como qualquer uuid.UUID.String()
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("pointID não parece um UUID: %q", id)
	}
}

func TestDiscoverProjectsListsSubdirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"proj-a", "proj-b", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{".hidden", "proj-a", "proj-b"}
	if len(got) != len(want) {
		t.Fatalf("esperava %v, veio %v (arquivo solto não deveria contar como projeto)", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("esperava %v, veio %v", want, got)
		}
	}
}

func TestDiscoverProjectsFallsBackToRootWhenNoSubdirs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Base(root)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("root sem subpastas deveria virar projeto único %q, veio %v", want, got)
	}
}

func TestDiscoverProjectsErrorsOnMissingRoot(t *testing.T) {
	_, err := DiscoverProjects(filepath.Join(t.TempDir(), "nao-existe"))
	if err == nil {
		t.Fatal("esperava erro pra root inexistente")
	}
}

func TestProjectDirUsesSubdirWhenItExists(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "meu-projeto"), 0755); err != nil {
		t.Fatal(err)
	}
	got := ProjectDir(root, "meu-projeto")
	want := filepath.Join(root, "meu-projeto")
	if got != want {
		t.Fatalf("esperava %q, veio %q", want, got)
	}
}

func TestProjectDirFallsBackToRootWhenSubdirMissing(t *testing.T) {
	root := t.TempDir()
	got := ProjectDir(root, "nao-existe")
	if got != root {
		t.Fatalf("esperava fallback pro root %q quando a subpasta não existe, veio %q", root, got)
	}
}

func TestProjectDirFallsBackWhenCandidateIsFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "arquivo-nao-pasta"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got := ProjectDir(root, "arquivo-nao-pasta")
	if got != root {
		t.Fatalf("candidato que é arquivo (não pasta) deveria cair no fallback do root, veio %q", got)
	}
}
