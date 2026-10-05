package chunker

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Chunk é um pedaço de arquivo pronto para embedding.
type Chunk struct {
	Project    string
	Path       string // caminho relativo ao projeto
	Language   string
	ChunkIndex int
	Content    string
	// Hash é o fingerprint do conteúdo (sha256 de Content), usado para
	// detectar chunks inalterados entre reindexações incrementais.
	Hash string
}

// Options controla o scan.
type Options struct {
	// Extensões/arquivos incluídos. Vazio = usa defaults.
	MaxFileBytes int // default 200KB
	ChunkSize    int // chars, default 1200
	Overlap      int // chars, default 200
}

func defaultOptions() Options {
	return Options{MaxFileBytes: 200 * 1024, ChunkSize: 1200, Overlap: 200}
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".next": true, "dist": true, "build": true, "tmp": true,
	"log": true, "logs": true, "storage": true, ".qdrant": true,
	"__pycache__": true, ".venv": true, "coverage": true,
}

var allowedExts = map[string]bool{
	".md": true, ".mdx": true, ".txt": true, ".rst": true,
	".rb": true, ".rake": true, ".erb": true, ".haml": true, ".slim": true,
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".vue": true,
	".go": true, ".py": true, ".java": true, ".kt": true, ".rs": true,
	".php": true, ".html": true, ".css": true, ".scss": true,
	".yml": true, ".yaml": true, ".toml": true, ".ini": true,
	".json": true, ".xml": true, ".sql": true, ".sh": true,
	".dockerfile": true, ".makefile": true,
}

var allowedBaseNames = map[string]bool{
	"dockerfile": true, "makefile": true, "rakefile": true, "gemfile": true,
	"readme": true, "changelog": true, "agents": true, "claude": true,
}

func languageFor(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(path), ext))
	if allowedBaseNames[base] {
		return base
	}
	return strings.TrimPrefix(ext, ".")
}

func shouldIndex(rel string, info fs.FileInfo) bool {
	if info.IsDir() {
		return false
	}
	if info.Size() == 0 {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		if skipDirs[p] {
			return false
		}
		if strings.HasPrefix(p, ".") && p != ".env.example" {
			// ignora dotfiles exceto .env.example
			return false
		}
	}
	// ignora lockfiles gigantes e minificados
	lower := strings.ToLower(filepath.Base(rel))
	for _, s := range []string{".min.js", ".min.css", "package-lock.json", "yarn.lock", "Gemfile.lock", ".map"} {
		if strings.HasSuffix(lower, s) {
			return false
		}
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if allowedExts[ext] {
		return true
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(rel), ext))
	if allowedBaseNames[base] {
		return true
	}
	// sem extensão mas conhecido (Dockerfile, Makefile...)
	if ext == "" && allowedBaseNames[strings.ToLower(filepath.Base(rel))] {
		return true
	}
	return false
}

// ScanProject percorre um projeto e retorna chunks.
func ScanProject(projectName, projectDir string, opt *Options) ([]Chunk, error) {
	o := defaultOptions()
	if opt != nil {
		if opt.MaxFileBytes > 0 {
			o.MaxFileBytes = opt.MaxFileBytes
		}
		if opt.ChunkSize > 0 {
			o.ChunkSize = opt.ChunkSize
		}
		if opt.Overlap > 0 {
			o.Overlap = opt.Overlap
		}
	}
	var chunks []Chunk
	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // ignora erros de permissão
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectDir, path)
		if !shouldIndex(rel, info) {
			return nil
		}
		if info.Size() > int64(o.MaxFileBytes) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		if !isMostlyText(text) {
			return nil
		}
		// header com contexto do arquivo ajuda o retrieval.
		// Arquivos com regra estrutural (rb/js/ts/md) saem em blocos por
		// método/classe/heading com escopo; demais usam janela com overlap.
		lang := languageFor(rel)
		header := fmt.Sprintf("Projeto: %s | Arquivo: %s | Linguagem: %s\n---\n", projectName, filepath.ToSlash(rel), lang)
		type piece struct {
			scope string
			body  string
		}
		var pieces []piece
		if blocks := splitStructured(text, lang); blocks != nil {
			for _, b := range packBlocks(blocks, o.ChunkSize) {
				pieces = append(pieces, piece{scope: b.scope, body: b.body})
			}
		} else {
			for _, p := range splitIntoChunks(text, o.ChunkSize, o.Overlap) {
				pieces = append(pieces, piece{body: p})
			}
		}
		for i, pc := range pieces {
			full := header
			if pc.scope != "" {
				full += "Escopo: " + pc.scope + "\n---\n"
			}
			full += pc.body
			h := sha256.Sum256([]byte(full))
			chunks = append(chunks, Chunk{
				Project:    projectName,
				Path:       filepath.ToSlash(rel),
				Language:   lang,
				ChunkIndex: i,
				Content:    full,
				Hash:       fmt.Sprintf("%x", h[:]),
			})
		}
		return nil
	})
	return chunks, err
}

func splitIntoChunks(text string, size, overlap int) []string {
	if len(text) <= size {
		return []string{text}
	}
	var out []string
	lines := strings.Split(text, "\n")
	var cur strings.Builder
	for _, ln := range lines {
		if cur.Len()+len(ln)+1 > size {
			out = append(out, cur.String())
			// overlap: mantém o sufixo
			prev := cur.String()
			cur.Reset()
			if len(prev) > overlap {
				cur.WriteString(prev[len(prev)-overlap:])
				cur.WriteString("\n")
			}
			// se uma única linha estoura, fatia no bruto
			if len(ln) > size {
				for j := 0; j < len(ln); j += size - overlap {
					end := j + size - overlap
					if end > len(ln) {
						end = len(ln)
					}
					out = append(out, ln[j:end])
				}
				continue
			}
		}
		cur.WriteString(ln)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func isMostlyText(s string) bool {
	if len(s) == 0 {
		return false
	}
	check := s
	if len(check) > 8000 {
		check = check[:8000]
	}
	bad := 0
	for i := 0; i < len(check); i++ {
		if check[i] == 0 {
			return false
		}
		if check[i] < 9 || (check[i] > 13 && check[i] < 32 && check[i] != 27) {
			bad++
		}
	}
	return float64(bad)/float64(len(check)) < 0.02
}
