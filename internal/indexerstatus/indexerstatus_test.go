package indexerstatus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "status.json")
	if err := EnsureDir(path); err != nil {
		t.Fatal(err)
	}
	want := Status{
		UpdatedAt:       "2026-10-06T01:00:00Z",
		IntervalSeconds: 600,
		CycleDurationMs: 5155,
		TotalChunks:     57990,
		OKCount:         4,
		FailCount:       0,
		Projects: []ProjectCycle{
			{Project: "atendimento", Chunks: 37878, DurationMs: 982},
			{Project: "neurolead", Chunks: 18982, DurationMs: 1006, Error: ""},
		},
		NextCycleAt: "2026-10-06T01:10:00Z",
	}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalChunks != want.TotalChunks || len(got.Projects) != len(want.Projects) || got.Projects[0].Project != "atendimento" {
		t.Fatalf("round-trip não bateu: got=%+v want=%+v", got, want)
	}
}

func TestReadMissingFileErrors(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "nao-existe.json"))
	if err == nil {
		t.Fatal("esperava erro lendo arquivo inexistente")
	}
}

func TestWriteIsAtomicNoLeftoverTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := Write(path, Status{TotalChunks: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("arquivo .tmp não deveria sobrar depois do rename")
	}
}
