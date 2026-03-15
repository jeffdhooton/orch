package generate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jeffdhooton/orch/internal/specgen/analyze"
	"github.com/jeffdhooton/orch/internal/specgen/prompt"
)

// Generator produces spec files by invoking the Claude CLI.
type Generator struct{}

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

// Generate produces spec files for each requested role.
func (g *Generator) Generate(ctx context.Context, opts GenerateOpts) error {
	// Check that claude CLI is available
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("claude CLI not found in PATH — install Claude Code: https://docs.anthropic.com/en/docs/claude-code")
	}

	// Create output directory
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
		output, err := callClaude(ctx, sp, userPrompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, " failed\n")
			return fmt.Errorf("generating %s spec: %w", role, err)
		}

		outPath := filepath.Join(opts.OutputDir, role+".md")
		if err := os.WriteFile(outPath, []byte(output), 0o644); err != nil {
			return fmt.Errorf("writing %s spec: %w", role, err)
		}

		fmt.Fprintf(os.Stderr, " done → %s\n", outPath)
	}

	return nil
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
