package history

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendKeepsLastMemCap(t *testing.T) {
	r := New("")
	r.memCap = 3
	for i := int64(1); i <= 5; i++ {
		r.Append(Sample{Points: i})
	}
	got := r.Last(10)
	if len(got) != 3 || got[0].Points != 3 || got[2].Points != 5 {
		t.Fatalf("ring deveria guardar só os 3 últimos: %+v", got)
	}
}

func TestLastDefaultsAndClamps(t *testing.T) {
	r := New("")
	r.Append(Sample{Points: 1})
	if got := r.Last(0); len(got) != 1 {
		t.Fatalf("n<=0 deveria virar default 120 (devolve o que tem): %d", len(got))
	}
	if got := r.Last(5000); len(got) != 1 {
		t.Fatalf("n>2000 deveria clampar, sem panic: %d", len(got))
	}
}

func TestAppendStampsTimestampWhenEmpty(t *testing.T) {
	r := New("")
	r.Append(Sample{Points: 9})
	if got := r.Last(1); len(got) != 1 || got[0].Timestamp == "" {
		t.Fatalf("timestamp vazio deveria ser preenchido: %+v", got)
	}
}

func TestFileRoundTripAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	r := New(path)
	for i := int64(1); i <= 5; i++ {
		r.Append(Sample{Points: i})
	}
	got := r.Last(2)
	if len(got) != 2 || got[0].Points != 4 || got[1].Points != 5 {
		t.Fatalf("tail deveria devolver os 2 últimos do arquivo: %+v", got)
	}
}

func TestSkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	_ = os.WriteFile(path, []byte("{\"points\":1}\nnao-e-json\n{\"points\":2}\n"), 0644)
	r := New(path)
	got := r.Last(10)
	if len(got) != 2 || got[0].Points != 1 || got[1].Points != 2 {
		t.Fatalf("linhas corrompidas deveriam ser puladas: %+v", got)
	}
}

func TestMissingFileFallsBackToMemory(t *testing.T) {
	// diretório inexistente: append no arquivo falha, mas a memória guarda.
	r := New(filepath.Join(t.TempDir(), "pasta-que-nao-existe", "h.jsonl"))
	r.Append(Sample{Points: 3})
	if got := r.Last(5); len(got) != 1 || got[0].Points != 3 {
		t.Fatalf("sem arquivo, deveria cair pro buffer em memória: %+v", got)
	}
}
