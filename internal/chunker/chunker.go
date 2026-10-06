package chunker

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	// ModTime é o mtime do arquivo de origem no momento do scan.
	ModTime time.Time
	// Size é o tamanho em bytes do arquivo de origem.
	Size int64
	// ChunkCount é o nº total de chunks que o arquivo gerou — usado para
	// detectar reindexações parciais (ex.: interrompidas no meio) antes de
	// confiar num skip por mtime na próxima execução.
	ChunkCount int
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

// configLikeExts são extensões de arquivo de config/dados (não código-fonte)
// — é nelas que segredos de verdade costumam aparecer (chave de API, senha
// de banco). A checagem de nome sensível abaixo só se aplica a essas
// extensões, pra não excluir código legítimo cujo nome só cita o conceito
// (ex.: "credential_resolver.rb" não é um segredo, é uma classe de serviço).
var configLikeExts = map[string]bool{
	".yml": true, ".yaml": true, ".json": true, ".ini": true,
	".toml": true, ".xml": true, ".txt": true,
}

// sensitiveBaseNames são nomes de arquivo de config amplamente conhecidos
// por guardar segredos reais (chave/senha), independente do conteúdo.
var sensitiveBaseNames = map[string]bool{
	"secrets.yml": true, "secrets.yaml": true,
	"database.yml": true, "database.yaml": true,
	"newrelic.yml": true, "scout_apm.yml": true, "elastic_apm.yml": true,
	"honeybadger.yml": true,
}

// isSensitiveConfig decide se um arquivo de config/dados parece guardar
// segredos reais e por isso não deve ser indexado — mesmo tendo uma
// extensão permitida. Arquivos claramente de exemplo/modelo (nome contém
// "example"/"sample"/"template") ficam de fora dessa checagem, igual ao
// tratamento já dado a .env.example.
func isSensitiveConfig(base string) bool {
	lower := strings.ToLower(base)
	if strings.Contains(lower, "example") || strings.Contains(lower, "sample") || strings.Contains(lower, "template") {
		return false
	}
	if sensitiveBaseNames[lower] {
		return true
	}
	return strings.Contains(lower, "secret") || strings.Contains(lower, "credential")
}

// isGeneratedPublicDir reconhece pastas de saída de build (bundles/assets
// compilados) dentro de public/ — não são código-fonte, só ruído pro RAG
// (e às vezes nem texto, são bundles minificados enormes). app/assets (que
// É código-fonte, Sass/JS escrito à mão) não é afetado: só pastas sob
// "public" entram nessa checagem.
func isGeneratedPublicDir(parentIsPublic bool, name string) bool {
	if !parentIsPublic {
		return false
	}
	return name == "assets" || strings.HasPrefix(name, "packs") || strings.HasPrefix(name, "vite")
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
	for i, p := range parts {
		if skipDirs[p] {
			return false
		}
		if strings.HasPrefix(p, ".") && p != ".env.example" {
			// ignora dotfiles exceto .env.example
			return false
		}
		if i > 0 && isGeneratedPublicDir(parts[i-1] == "public", p) {
			// pasta de build (public/assets, public/packs*, public/vite*)
			return false
		}
	}
	// ignora lockfiles gigantes e minificados
	lower := strings.ToLower(filepath.Base(rel))
	if lower == ".env.example" {
		// exceção explícita: é um template, não segredo — e não bate em
		// nenhuma extensão/nome permitido por si só, então precisa sair
		// daqui antes de cair no allowlist de extensão abaixo.
		return true
	}
	for _, s := range []string{".min.js", ".min.css", "package-lock.json", "yarn.lock", "Gemfile.lock", ".map"} {
		if strings.HasSuffix(lower, s) {
			return false
		}
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if allowedExts[ext] {
		if configLikeExts[ext] && isSensitiveConfig(filepath.Base(rel)) {
			return false
		}
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

// fileEntry é o resultado leve (sem conteúdo) da passada de coleta.
type fileEntry struct {
	rel     string
	abs     string
	modTime time.Time
	size    int64
}

// collectFiles varre o projeto e coleta metadados (sem ler conteúdo) de
// todo arquivo indexável, ordenados por mtime decrescente (mais recentes
// primeiro). Usa os.Stat (segue symlink) em vez de d.Info() (lstat) —
// senão o mtime do link nunca mudaria quando o conteúdo do alvo muda.
func collectFiles(projectDir string, maxFileBytes int) ([]fileEntry, error) {
	var files []fileEntry
	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // ignora erros de permissão
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			// pastas-ponto (.git, .github, .vscode, node_modules internos
			// de ferramentas, etc.) nunca são indexadas (shouldIndex já
			// filtraria por arquivo, mas nem vale a pena descer e listar).
			// path != projectDir pra não podar a própria raiz do projeto,
			// caso o nome da pasta do projeto comece com ".".
			if path != projectDir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			parent := filepath.Base(filepath.Dir(path))
			if isGeneratedPublicDir(parent == "public", d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		lstatInfo, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectDir, path)
		if !shouldIndex(rel, lstatInfo) {
			return nil
		}
		info, err := os.Stat(path) // segue symlink; erro = link quebrado
		if err != nil {
			return nil
		}
		if info.Size() > int64(maxFileBytes) {
			return nil
		}
		files = append(files, fileEntry{
			rel:     filepath.ToSlash(rel),
			abs:     path,
			modTime: info.ModTime(),
			size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
	return files, nil
}

// ScanProject percorre um projeto (arquivos modificados mais recentemente
// primeiro) e chama onChunk para cada chunk — sem materializar a árvore
// inteira em memória. Se shouldSkip(rel, modTime, size) retornar true para
// um arquivo, ele não é lido (nem hasheado) — útil para pular arquivos
// comprovadamente inalterados numa reindexação incremental. shouldSkip
// pode ser nil (nunca pula). Se onChunk retornar erro, o scan é abortado e
// o erro sobe para o chamador.
func ScanProject(projectName, projectDir string, opt *Options, shouldSkip func(rel string, modTime time.Time, size int64) bool, onChunk func(Chunk) error) error {
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
	files, err := collectFiles(projectDir, o.MaxFileBytes)
	if err != nil {
		return err
	}
	for _, f := range files {
		if shouldSkip != nil && shouldSkip(f.rel, f.modTime, f.size) {
			continue
		}
		data, err := os.ReadFile(f.abs)
		if err != nil {
			continue
		}
		text := string(data)
		if !isMostlyText(text) {
			continue
		}
		// header com contexto do arquivo ajuda o retrieval.
		// Arquivos com regra estrutural (rb/js/ts/md) saem em blocos por
		// método/classe/heading com escopo; demais usam janela com overlap.
		lang := languageFor(f.rel)
		header := fmt.Sprintf("Projeto: %s | Arquivo: %s | Linguagem: %s\n---\n", projectName, f.rel, lang)
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
			c := Chunk{
				Project:    projectName,
				Path:       f.rel,
				Language:   lang,
				ChunkIndex: i,
				Content:    full,
				Hash:       fmt.Sprintf("%x", h[:]),
				ModTime:    f.modTime,
				Size:       f.size,
				ChunkCount: len(pieces),
			}
			if err := onChunk(c); err != nil {
				return err
			}
		}
	}
	return nil
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
