package generate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeffdhooton/orch/internal/specgen/analyze"
	"github.com/jeffdhooton/orch/internal/specgen/prompt"
)

// Generator produces spec files by invoking the Claude CLI.
type Generator struct {
	Verbose bool
}

// GenerateOpts configures spec generation.
type GenerateOpts struct {
	Analysis  *analyze.Analysis
	Task      string
	Roles     []string
	OutputDir string
}

// New creates a Generator.
func New() *Generator {
	return &Generator{}
}

func (g *Generator) logf(format string, args ...any) {
	if g.Verbose {
		fmt.Fprintf(os.Stderr, "[verbose] "+format+"\n", args...)
	}
}

// Generate produces spec files for each requested role.
func (g *Generator) Generate(ctx context.Context, opts GenerateOpts) error {
	// Check that claude CLI is available
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude CLI not found in PATH — install Claude Code: https://docs.anthropic.com/en/docs/claude-code")
	}
	g.logf("claude CLI found at %s", claudePath)

	// Create output directory
	g.logf("creating output directory: %s", opts.OutputDir)
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	systemPrompts := map[string]string{
		"engineer": engineerSystemPrompt,
		"pm":       pmSystemPrompt,
		"reviewer": reviewerSystemPrompt,
	}

	for _, role := range opts.Roles {
		sp, ok := systemPrompts[role]
		if !ok {
			return fmt.Errorf("unknown role: %q (valid: engineer, pm, reviewer)", role)
		}

		fmt.Fprintf(os.Stderr, "Generating %s spec...", role)

		userPrompt := prompt.BuildUserPrompt(opts.Analysis, opts.Task, role)
		g.logf("system prompt length: %d chars", len(sp))
		g.logf("user prompt length: %d chars", len(userPrompt))
		g.logf("calling claude CLI: claude -p --system-prompt <...> --output-format text")

		start := time.Now()
		output, err := callClaude(ctx, sp, userPrompt)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Fprintf(os.Stderr, " failed\n")
			g.logf("claude call failed after %s: %v", elapsed, err)
			return fmt.Errorf("generating %s spec: %w", role, err)
		}

		g.logf("claude responded in %s (%d chars)", elapsed, len(output))

		outPath := filepath.Join(opts.OutputDir, role+".md")
		if err := os.WriteFile(outPath, []byte(output), 0o644); err != nil {
			return fmt.Errorf("writing %s spec: %w", role, err)
		}

		fmt.Fprintf(os.Stderr, " done → %s\n", outPath)
	}

	return nil
}

// GenerateSlug asks Claude to produce a short directory-name slug from a task description.
// If verbose is true, progress is logged to stderr.
func GenerateSlug(ctx context.Context, task string, verbose bool) (string, error) {
	const systemPrompt = `You are a slug generator. Given a task description, output a short (2-5 word) kebab-case slug suitable for a directory name. Output ONLY the slug, nothing else. Examples:
- "Implement OAuth2 authentication with refresh tokens" → "oauth2-auth"
- "Fix the bug where users can't upload images larger than 5MB" → "fix-image-upload"
- "Add Redis caching layer for API responses" → "redis-api-cache"`

	if verbose {
		fmt.Fprintf(os.Stderr, "[verbose] calling claude CLI for slug generation...\n")
	}
	start := time.Now()
	slug, err := callClaude(ctx, systemPrompt, task)
	elapsed := time.Since(start)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[verbose] slug generation failed after %s: %v\n", elapsed, err)
		}
		return "", fmt.Errorf("generating slug: %w", err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[verbose] slug generated in %s: %q\n", elapsed, slug)
	}

	// Sanitize the output to ensure it's filesystem-safe
	slug = strings.ToLower(strings.TrimSpace(slug))
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, slug)
	slug = strings.Trim(slug, "-")

	if slug == "" {
		return "", fmt.Errorf("claude returned empty slug")
	}
	return slug, nil
}

func callClaude(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p",
		"--system-prompt", systemPrompt,
		"--output-format", "text",
	)
	cmd.Stdin = strings.NewReader(userPrompt)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%w: %s", err, string(exitErr.Stderr))
		}
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}
