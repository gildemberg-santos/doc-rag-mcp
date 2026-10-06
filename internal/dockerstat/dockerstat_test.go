package dockerstat

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newFakeDockerSocket sobe um httptest.Server de verdade sobre um socket
// Unix (não TCP) — exercita o mesmo transporte (net.Dialer "unix") que o
// Client usa contra o docker.sock real, sem precisar de Docker instalado.
func newFakeDockerSocket(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: handler}}
	srv.Start()
	t.Cleanup(srv.Close)
	return New(sockPath)
}

func TestListContainersParsesAndStripsNamePrefix(t *testing.T) {
	c := newFakeDockerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"Id": "abc123", "Names": []string{"/doc-rag-mcp"}, "Image": "doc-rag-mcp-mcp-server", "State": "running", "Status": "Up 3 hours", "Created": 1234567890},
		})
	})
	containers, err := c.ListContainers(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 {
		t.Fatalf("esperava 1 container, veio %d", len(containers))
	}
	got := containers[0]
	if got.Name != "doc-rag-mcp" {
		t.Errorf("esperava nome sem a barra, veio %q", got.Name)
	}
	if got.State != "running" || got.Status != "Up 3 hours" {
		t.Errorf("state/status errados: %+v", got)
	}
}

func TestListContainersFiltersByComposeProject(t *testing.T) {
	var gotQuery string
	c := newFakeDockerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	})
	if _, err := c.ListContainers(context.Background(), "doc-rag-mcp"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "filters=") || !strings.Contains(gotQuery, "com.docker.compose.project") {
		t.Fatalf("esperava query com filtro de label, veio %q", gotQuery)
	}
}

func TestAvailableTrueWhenSocketResponds(t *testing.T) {
	c := newFakeDockerSocket(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	})
	if !c.Available(context.Background()) {
		t.Fatal("esperava Available=true com o socket fake respondendo")
	}
}

func TestAvailableFalseWhenSocketMissing(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "nao-existe.sock"))
	if c.Available(context.Background()) {
		t.Fatal("esperava Available=false pra um socket que não existe")
	}
}
