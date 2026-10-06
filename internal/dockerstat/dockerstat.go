// Package dockerstat lê status de containers via o Docker Engine API, por
// cima do socket Unix do daemon. Só leitura (GET) — nunca cria, para ou
// remove nada. Não usa o SDK oficial (dependência pesada) nem o CLI
// `docker` (não está na imagem): é HTTP puro sobre um socket Unix, no
// mesmo espírito do vector.Client falando REST direto com o Qdrant.
//
// Aviso de segurança: montar o socket do Docker num container — mesmo
// como ":ro" — dá acesso à API completa do daemon (o ":ro" só afeta o
// arquivo do socket em si, não as permissões da API do lado de dentro).
// Este cliente só implementa chamadas de leitura; a superfície de
// segurança real depende de nunca estender isso com POST/DELETE.
package dockerstat

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client fala com o daemon Docker via socket Unix.
type Client struct {
	SocketPath string
	HTTP       *http.Client
}

// New cria um Client. socketPath típico: /var/run/docker.sock.
func New(socketPath string) *Client {
	return &Client{
		SocketPath: socketPath,
		HTTP: &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// Container é um resumo de um container, já sem o "/" que o Docker
// prefixa em Names.
type Container struct {
	ID      string
	Name    string
	Image   string
	State   string // "running", "exited", "restarting", ...
	Status  string // "Up 3 hours", "Exited (137) 2 minutes ago"
	Created int64  // unix seconds
}

// ListContainers lista containers (todos, incluindo parados). Quando
// composeProject != "", filtra pela label com.docker.compose.project —
// evita listar containers de outros projetos Docker na mesma máquina.
func (c *Client) ListContainers(ctx context.Context, composeProject string) ([]Container, error) {
	u := "http://unix/containers/json?all=true"
	if composeProject != "" {
		filters := fmt.Sprintf(`{"label":["com.docker.compose.project=%s"]}`, composeProject)
		u += "&filters=" + url.QueryEscape(filters)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker socket indisponível (%s): %w", c.SocketPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API respondeu status=%d", resp.StatusCode)
	}
	var raw []struct {
		ID      string   `json:"Id"`
		Names   []string `json:"Names"`
		Image   string   `json:"Image"`
		State   string   `json:"State"`
		Status  string   `json:"Status"`
		Created int64    `json:"Created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Container, len(raw))
	for i, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = strings.TrimPrefix(r.Names[0], "/")
		}
		out[i] = Container{ID: r.ID, Name: name, Image: r.Image, State: r.State, Status: r.Status, Created: r.Created}
	}
	return out, nil
}

// Available confere rapidamente se o socket está acessível — pra quem usa
// tratar "Docker não montado nesse deployment" como ausência normal, não
// como erro do /status inteiro.
func (c *Client) Available(ctx context.Context) bool {
	_, err := c.ListContainers(ctx, "")
	return err == nil
}
