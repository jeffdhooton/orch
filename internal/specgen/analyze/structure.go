package analyze

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Directories to skip during structure analysis.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".next":        true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	".cache":       true,
	".turbo":       true,
	"target":       true, // Rust/Java
	".vercel":      true,
}

// Key file patterns that indicate important entry points or configs.
var keyFilePatterns = []string{
	"main.go", "main.ts", "main.js",
	"index.ts", "index.js", "index.tsx",
	"app.ts", "app.js", "app.tsx",
	"server.go", "server.ts", "server.js",
	"Makefile", "Taskfile.yml",
}

// MapStructure walks the project directory and categorizes files.
func MapStructure(dir string) StructureInfo {
	s := StructureInfo{
		FilesByExt: make(map[string]int),
	}

	const maxFiles = 500

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}

		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}

		// Skip excluded directories
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".github") {
				return filepath.SkipDir
			}
			// Collect top-level directories
			if filepath.Dir(rel) == "." {
				s.TopLevelDirs = append(s.TopLevelDirs, name)
			}
			return nil
		}

		if s.TotalFiles >= maxFiles {
			return nil // stop counting but keep walking for dirs
		}

		s.TotalFiles++

		// Count by extension
		ext := filepath.Ext(d.Name())
		if ext != "" {
			s.FilesByExt[ext]++
		}

		// Detect key files
		name := d.Name()
		for _, pattern := range keyFilePatterns {
			if name == pattern {
				s.KeyFiles = append(s.KeyFiles, rel)
				break
			}
		}

		// Detect test files
		if isTestFile(name) {
			s.TestFiles = append(s.TestFiles, rel)
		}

		return nil
	})

	// Cap test files list for prompt size
	if len(s.TestFiles) > 20 {
		s.TestFiles = s.TestFiles[:20]
	}
	if len(s.KeyFiles) > 20 {
		s.KeyFiles = s.KeyFiles[:20]
	}

	return s
}

func isTestFile(name string) bool {
	return strings.HasSuffix(name, "_test.go") ||
		strings.HasSuffix(name, ".test.ts") ||
		strings.HasSuffix(name, ".test.tsx") ||
		strings.HasSuffix(name, ".test.js") ||
		strings.HasSuffix(name, ".test.jsx") ||
		strings.HasSuffix(name, ".spec.ts") ||
		strings.HasSuffix(name, ".spec.tsx") ||
		strings.HasSuffix(name, ".spec.js") ||
		strings.HasSuffix(name, ".spec.jsx") ||
		strings.HasSuffix(name, "_test.py") ||
		strings.HasPrefix(name, "test_")
}
