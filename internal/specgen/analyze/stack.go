package analyze

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// DetectStack identifies the tech stack from config files.
func DetectStack(dir string) StackInfo {
	s := StackInfo{}

	// Check for config files that tell us about the stack
	configs := map[string]bool{}
	configChecks := []string{
		"tailwind.config.js", "tailwind.config.ts", "tailwind.config.mjs",
		"Dockerfile", "docker-compose.yml", "docker-compose.yaml",
		".github/workflows",
		"astro.config.mjs", "astro.config.ts",
		"next.config.js", "next.config.mjs", "next.config.ts",
		"vite.config.js", "vite.config.ts",
		"tsconfig.json",
		"sanity.config.ts", "sanity.config.js",
		"prisma/schema.prisma",
		"playwright.config.ts", "playwright.config.js",
		"cypress.json", "cypress.config.ts",
	}

	for _, c := range configChecks {
		path := filepath.Join(dir, c)
		if _, err := os.Stat(path); err == nil {
			configs[c] = true
			s.ConfigFiles = append(s.ConfigFiles, c)
		}
	}

	// Detect primary language and stack
	switch {
	case fileExists(dir, "go.mod"):
		s = detectGo(dir, s)
	case fileExists(dir, "package.json"):
		s = detectNode(dir, s, configs)
	case fileExists(dir, "Cargo.toml"):
		s.Language = "rust"
		s.BuildCmd = "cargo build"
		s.TestCmd = "cargo test"
		s.LintCmd = "cargo clippy"
	case fileExists(dir, "pyproject.toml") || fileExists(dir, "requirements.txt"):
		s = detectPython(dir, s)
	default:
		s.Language = "unknown"
		s.BuildCmd = "# no build command detected"
		s.TestCmd = "# no test command detected"
	}

	// Detect framework from config files
	if s.Framework == "" {
		switch {
		case configs["astro.config.mjs"] || configs["astro.config.ts"]:
			s.Framework = "astro"
		case configs["next.config.js"] || configs["next.config.mjs"] || configs["next.config.ts"]:
			s.Framework = "next"
		case configs["vite.config.js"] || configs["vite.config.ts"]:
			s.Framework = "vite"
		}
	}

	return s
}

func detectGo(dir string, s StackInfo) StackInfo {
	s.Language = "go"
	s.BuildCmd = "go build ./..."
	s.TestCmd = "go test ./..."
	s.LintCmd = "go vet ./..."

	content, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return s
	}

	lines := strings.Split(string(content), "\n")
	inRequire := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "require (") || strings.HasPrefix(line, "require(") {
			inRequire = true
			continue
		}
		if inRequire && line == ")" {
			inRequire = false
			continue
		}
		if inRequire {
			// Skip indirect dependencies
			if strings.Contains(line, "// indirect") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				dep := parts[0]
				s.Dependencies = append(s.Dependencies, dep)
			}
		}
	}

	return s
}

func detectNode(dir string, s StackInfo, configs map[string]bool) StackInfo {
	s.Language = "node"
	s.BuildCmd = "npm run build"
	s.TestCmd = "npm test"
	s.LintCmd = "npm run lint"

	content, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return s
	}

	var pkg struct {
		Scripts      map[string]string `json:"scripts"`
		Dependencies map[string]string `json:"dependencies"`
		DevDeps      map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(content, &pkg); err != nil {
		return s
	}

	// Detect framework from dependencies
	allDeps := make(map[string]string)
	for k, v := range pkg.Dependencies {
		allDeps[k] = v
	}
	for k, v := range pkg.DevDeps {
		allDeps[k] = v
	}

	switch {
	case allDeps["astro"] != "":
		s.Framework = "astro"
	case allDeps["next"] != "":
		s.Framework = "next"
	case allDeps["@remix-run/react"] != "":
		s.Framework = "remix"
	case allDeps["nuxt"] != "":
		s.Framework = "nuxt"
	case allDeps["svelte"] != "":
		s.Framework = "svelte"
	case allDeps["vue"] != "":
		s.Framework = "vue"
	case allDeps["react"] != "":
		s.Framework = "react"
	}

	// Use actual scripts if available
	if _, ok := pkg.Scripts["build"]; !ok {
		s.BuildCmd = "# no build script in package.json"
	}
	if _, ok := pkg.Scripts["test"]; !ok {
		s.TestCmd = "# no test script in package.json"
	}
	if _, ok := pkg.Scripts["lint"]; !ok {
		s.LintCmd = ""
	}

	// Top dependencies
	for dep := range pkg.Dependencies {
		s.Dependencies = append(s.Dependencies, dep)
	}

	return s
}

func detectPython(dir string, s StackInfo) StackInfo {
	s.Language = "python"
	s.BuildCmd = "# no build step"
	s.TestCmd = "pytest"
	s.LintCmd = "ruff check ."

	if fileExists(dir, "pyproject.toml") {
		content, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
		if err == nil {
			text := string(content)
			if strings.Contains(text, "django") || strings.Contains(text, "Django") {
				s.Framework = "django"
			} else if strings.Contains(text, "fastapi") || strings.Contains(text, "FastAPI") {
				s.Framework = "fastapi"
			} else if strings.Contains(text, "flask") || strings.Contains(text, "Flask") {
				s.Framework = "flask"
			}
		}
	}

	return s
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}
