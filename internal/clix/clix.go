// Package clix reúne helpers de CLI compartilhados pelos binários
// (server, indexer, docgen): resolução de alvos, primeiro-não-vazio e
// rótulo de saída. Antes cada main tinha sua cópia (firstNonEmpty ×3,
// resolveTargets ×2) divergindo aos poucos.
package clix

import (
	"log"
	"os"

	"doc-rag-mcp/internal/indexer"
)

// Target é um projeto a processar (nome + diretório local).
type Target struct {
	Name string
	Dir  string
}

// FirstNonEmpty devolve o primeiro valor não-vazio (flag > env > default).
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// OutDirLabel rotula o destino do docgen (vazio = stdout).
func OutDirLabel(out string) string {
	if out == "" {
		return "stdout"
	}
	return out
}

// ResolveTargets espelha a resolução do indexer: --all redescobre a cada
// execução; single-project deriva path de projectsRoot (ou PROJECT_NAME).
func ResolveTargets(projectsRoot string, all bool, project, path string) []Target {
	if all {
		projects, err := indexer.DiscoverProjects(projectsRoot)
		if err != nil {
			log.Fatalf("discover: %v", err)
		}
		var out []Target
		for _, p := range projects {
			out = append(out, Target{Name: p, Dir: indexer.ProjectDir(projectsRoot, p)})
		}
		return out
	}
	if path == "" && project != "" {
		path = indexer.ProjectDir(projectsRoot, project)
	}
	if path == "" {
		path = projectsRoot
	}
	if project == "" {
		if env := os.Getenv("PROJECT_NAME"); env != "" {
			project = env
		} else if projects, _ := indexer.DiscoverProjects(path); len(projects) == 1 {
			project = projects[0]
		} else {
			project = "default"
		}
	}
	return []Target{{Name: project, Dir: path}}
}
