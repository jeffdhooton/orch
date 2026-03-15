package prompt

import (
	"fmt"
	"strings"

	"github.com/jeffdhooton/orch/internal/specgen/analyze"
)

// BuildUserPrompt constructs the user-facing prompt sent to Claude for spec generation.
func BuildUserPrompt(analysis *analyze.Analysis, task string, role string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("## Task\n%s\n\n", task))
	b.WriteString("## Codebase Analysis\n\n")
	b.WriteString(analysis.FormatAsText())
	b.WriteString(fmt.Sprintf("\n## Generate the %s spec\n", role))
	b.WriteString(fmt.Sprintf("Generate a complete, actionable %s spec for the task above based on this codebase analysis. ", role))
	b.WriteString("Reference actual file paths from the analysis. Use the correct build/test/lint commands for this stack. ")
	b.WriteString("Output only the markdown spec content — no preamble, no wrapping code fences.\n")

	return b.String()
}
