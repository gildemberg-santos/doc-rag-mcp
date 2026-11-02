// Package watcher monitora diretórios de projetos em tempo real via fsnotify,
// disparando um callback sempre que arquivos indexáveis são criados,
// modificados, renomeados ou removidos.
//
// Usado pelo modo --watch do indexer para reduzir a latência entre uma
// edição no disco e sua indexação no Qdrant, de minutos (polling) para
// segundos (event-driven). O polling continua como rede de segurança.
package watcher

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceDefault é o tempo de espera após o último evento antes de disparar
// o callback. Evita múltiplos disparos em edições rápidas (ex.: sed -i).
const debounceDefault = 500 * time.Millisecond

// Watcher monitora um ou mais diretórios e encaminha caminhos de arquivos
// alterados para um callback.
type Watcher struct {
	watcher  *fsnotify.Watcher
	mu       sync.Mutex
	dirs     map[string]bool
	debounce time.Duration
	pending  map[string]time.Time
	timer    *time.Timer
	onChange func(paths []string)
}

// New cria um Watcher com o intervalo de debounce informado.
// Se debounce <= 0, usa o default (500ms).
func New(debounce time.Duration, onChange func([]string)) (*Watcher, error) {
	if debounce <= 0 {
		debounce = debounceDefault
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("criar watcher fsnotify: %w", err)
	}
	return &Watcher{
		watcher:  w,
		dirs:     make(map[string]bool),
		debounce: debounce,
		pending:  make(map[string]time.Time),
		onChange: onChange,
	}, nil
}

// Add passa a monitorar um diretório (fsnotify propaga para subdiretórios).
func (w *Watcher) Add(dir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dirs[dir] {
		return nil
	}
	if err := w.watcher.Add(dir); err != nil {
		return fmt.Errorf("adicionar diretório %s ao watcher: %w", dir, err)
	}
	w.dirs[dir] = true
	log.Printf("👀 watcher: monitorando %s", dir)
	return nil
}

// Run loop principal do watcher. Bloqueia até o contexto ser cancelado.
func (w *Watcher) Run(ctx context.Context) {
	defer w.watcher.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("🚫 watcher error: %v", err)
		}
	}
}

// Close fecha o watcher subjacente.
func (w *Watcher) Close() error {
	return w.watcher.Close()
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	name := event.Name
	// Ignora arquivos/diretórios ocultos (exceto .env.example, que já é
	// tratada pelo chunker; aqui só ignoramos para evitar ruído de watcher).
	base := filepath.Base(name)
	if strings.HasPrefix(base, ".") && base != ".env.example" {
		return
	}
	// Só arquivos indexáveis pelo chunker disparam reindex.
	if !isIndexable(name) {
		return
	}

	w.mu.Lock()
	w.pending[name] = time.Now()
	// Reinicia o timer de debounce a cada evento novo.
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, func() {
		w.flush()
	})
	w.mu.Unlock()
}

func (w *Watcher) flush() {
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.mu.Unlock()
		return
	}
	paths := make([]string, 0, len(w.pending))
	for p := range w.pending {
		paths = append(paths, p)
	}
	sort.Strings(paths) // ordem determinística para logs e reprocessamento
	w.pending = make(map[string]time.Time)
	w.timer = nil
	w.mu.Unlock()

	if w.onChange != nil {
		w.onChange(paths)
	}
}

// isIndexable informa se um caminho deve ser monitorado (mesmas regras de
// extensão/base do chunker, sem ler conteúdo).
func isIndexable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".rb", ".rake", ".erb", ".haml", ".slim",
		".js", ".jsx", ".ts", ".tsx", ".vue",
		".go", ".py", ".java", ".kt", ".rs",
		".php", ".html", ".css", ".scss",
		".yml", ".yaml", ".toml", ".ini",
		".json", ".xml", ".sql", ".sh",
		".md", ".mdx", ".txt", ".rst",
		".dockerfile", ".makefile":
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile", "makefile", "rakefile", "gemfile", "readme", "changelog":
		return true
	}
	return false
}
