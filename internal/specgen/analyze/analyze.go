package analyze

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Analysis holds the complete codebase analysis result.
type Analysis struct {
	Dir           string
	Stack         StackInfo
	Structure     StructureInfo
	Git           GitInfo
	Documentation []DocFile
}

// StackInfo describes the detected tech stack.
type StackInfo struct {
	Language     string
	Framework    string
	BuildCmd     string
	TestCmd      string
	LintCmd      string
	Dependencies []string
	ConfigFiles  []string
}

// StructureInfo describes the project's file structure.
type StructureInfo struct {
	TopLevelDirs []string
	KeyFiles     []string
	TestFiles    []string
	TotalFiles   int
	FilesByExt   map[string]int
}

// GitInfo describes the git repository state.
type GitInfo struct {
	Branch         string
	RecentCommits  []string
	HasUncommitted bool
	RemoteURL      string
}

// DocFile holds a documentation file's path and content.
type DocFile struct {
	Path    string
	Content string
}

// Analyze runs all sub-analyzers on the given directory.
func Analyze(dir string) (*Analysis, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving path: %w", err)
	}

	a := &Analysis{Dir: absDir}

	// Stack detection
	a.Stack = DetectStack(absDir)

	// Structure mapping
	a.Structure = MapStructure(absDir)

	// Git state
	a.Git = CollectGitInfo(absDir)

	// Documentation
	a.Documentation = collectDocs(absDir)

	return a, nil
}

// FormatAsText renders the analysis as readable markdown for use in LLM prompts.
func (a *Analysis) FormatAsText() string {
	var b strings.Builder

	b.WriteString("## Tech Stack\n")
	b.WriteString(fmt.Sprintf("- Language: %s\n", a.Stack.Language))
	if a.Stack.Framework != "" {
		b.WriteString(fmt.Sprintf("- Framework: %s\n", a.Stack.Framework))
	}
	b.WriteString(fmt.Sprintf("- Build command: %s\n", a.Stack.BuildCmd))
	b.WriteString(fmt.Sprintf("- Test command: %s\n", a.Stack.TestCmd))
	if a.Stack.LintCmd != "" {
		b.WriteString(fmt.Sprintf("- Lint command: %s\n", a.Stack.LintCmd))
	}
	if len(a.Stack.Dependencies) > 0 {
		b.WriteString(fmt.Sprintf("- Key dependencies: %s\n", strings.Join(a.Stack.Dependencies, ", ")))
	}
	if len(a.Stack.ConfigFiles) > 0 {
		b.WriteString(fmt.Sprintf("- Config files: %s\n", strings.Join(a.Stack.ConfigFiles, ", ")))
	}
	b.WriteString("\n")

	b.WriteString("## Project Structure\n")
	b.WriteString(fmt.Sprintf("- Top-level directories: %s\n", strings.Join(a.Structure.TopLevelDirs, ", ")))
	b.WriteString(fmt.Sprintf("- Total files: %d\n", a.Structure.TotalFiles))
	if len(a.Structure.KeyFiles) > 0 {
		b.WriteString("- Key files:\n")
		for _, f := range a.Structure.KeyFiles {
			b.WriteString(fmt.Sprintf("  - %s\n", f))
		}
	}
	if len(a.Structure.TestFiles) > 0 {
		b.WriteString("- Test files:\n")
		for _, f := range a.Structure.TestFiles {
			b.WriteString(fmt.Sprintf("  - %s\n", f))
		}
	}
	if len(a.Structure.FilesByExt) > 0 {
		b.WriteString("- Files by extension:\n")
		for ext, count := range a.Structure.FilesByExt {
			b.WriteString(fmt.Sprintf("  - %s: %d\n", ext, count))
		}
	}
	b.WriteString("\n")

	if a.Git.Branch != "" {
		b.WriteString("## Git State\n")
		b.WriteString(fmt.Sprintf("- Branch: %s\n", a.Git.Branch))
		b.WriteString(fmt.Sprintf("- Uncommitted changes: %v\n", a.Git.HasUncommitted))
		if a.Git.RemoteURL != "" {
			b.WriteString(fmt.Sprintf("- Remote: %s\n", a.Git.RemoteURL))
		}
		if len(a.Git.RecentCommits) > 0 {
			b.WriteString("- Recent commits:\n")
			for _, c := range a.Git.RecentCommits {
				b.WriteString(fmt.Sprintf("  - %s\n", c))
			}
		}
		b.WriteString("\n")
	}

	if len(a.Documentation) > 0 {
		b.WriteString("## Documentation\n")
		for _, doc := range a.Documentation {
			b.WriteString(fmt.Sprintf("### %s\n", doc.Path))
			b.WriteString(doc.Content)
			b.WriteString("\n\n")
		}
	}

	return b.String()
}

func collectDocs(dir string) []DocFile {
	docFiles := []string{"README.md", "CLAUDE.md"}
	var docs []DocFile

	for _, name := range docFiles {
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Truncate to first 100 lines
		lines := strings.Split(string(content), "\n")
		if len(lines) > 100 {
			lines = lines[:100]
			lines = append(lines, "\n... (truncated)")
		}
		docs = append(docs, DocFile{
			Path:    name,
			Content: strings.Join(lines, "\n"),
		})
	}

	return docs
}
