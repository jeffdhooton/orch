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
	Model   string // e.g. "sonnet", "haiku", "opus"
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

		fmt.Fprintf(os.Stderr, "Generating %s spec ", role)

		userPrompt := prompt.BuildUserPrompt(opts.Analysis, opts.Task, role)
		g.logf("system prompt length: %d chars", len(sp))
		g.logf("user prompt length: %d chars", len(userPrompt))

		// Run claude call with a spinner so the user knows it's working.
		start := time.Now()
		type result struct {
			output string
			err    error
		}
		ch := make(chan result, 1)
		go func() {
			out, err := g.callClaude(ctx, sp, userPrompt)
			ch <- result{out, err}
		}()

		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		ticker := time.NewTicker(100 * time.Millisecond)
		fi := 0
		var res result
	spin:
		for {
			select {
			case res = <-ch:
				ticker.Stop()
				break spin
			case <-ticker.C:
				elapsed := time.Since(start).Truncate(time.Second)
				fmt.Fprintf(os.Stderr, "\r%s Generating %s spec %s", frames[fi%len(frames)], role, elapsed)
				fi++
			}
		}

		elapsed := time.Since(start)

		if res.err != nil {
			fmt.Fprintf(os.Stderr, "\r✗ Generating %s spec failed (%s)\n", role, elapsed.Truncate(time.Second))
			g.logf("claude call failed after %s: %v", elapsed, res.err)
			return fmt.Errorf("generating %s spec: %w", role, res.err)
		}

		g.logf("claude responded in %s (%d chars)", elapsed, len(res.output))

		outPath := filepath.Join(opts.OutputDir, role+".md")
		if err := os.WriteFile(outPath, []byte(res.output), 0o644); err != nil {
			return fmt.Errorf("writing %s spec: %w", role, err)
		}

		fmt.Fprintf(os.Stderr, "\r✓ Generated %s spec (%s) → %s\n", role, elapsed.Truncate(time.Second), outPath)
	}

	return nil
}

func (g *Generator) callClaude(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	args := []string{
		"-p",
		"--dangerously-skip-permissions",
		"--strict-mcp-config",
		"--system-prompt", systemPrompt,
		"--output-format", "text",
	}
	if g.Model != "" {
		args = append(args, "--model", g.Model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(userPrompt)

	// Log the actual command (mask the system prompt since it's huge)
	logArgs := make([]string, len(args))
	copy(logArgs, args)
	for i, a := range logArgs {
		if a == "--system-prompt" && i+1 < len(logArgs) {
			logArgs[i+1] = "<...>"
		}
	}
	g.logf("calling claude CLI: claude %s", strings.Join(logArgs, " "))

	// Pass stderr through so the user sees any warnings, errors, or prompts
	// from claude in real time instead of silently swallowing them.
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}
