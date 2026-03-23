package prompt

import (
	"fmt"
	"strings"

	"github.com/jeffdhooton/orch/internal/specgen/analyze"
)

// BuildUserPrompt constructs the user-facing prompt sent to Claude for spec generation.
func BuildUserPrompt(analysis *analyze.Analysis, task string, role string, skillCommands []string, planContent string, roles ...string) string {
	var b strings.Builder

	if planContent != "" {
		b.WriteString("## Plan Document\n\n")
		b.WriteString(planContent)
		b.WriteString("\n\n")

		if task != "" {
			b.WriteString(fmt.Sprintf("## Additional Context\n%s\n\n", task))
		}

		b.WriteString("## Codebase Analysis\n\n")
		b.WriteString(analysis.FormatAsText())
	} else {
		b.WriteString(fmt.Sprintf("## Task\n%s\n\n", task))
		b.WriteString("## Codebase Analysis\n\n")
		b.WriteString(analysis.FormatAsText())
	}

	if len(skillCommands) > 0 {
		b.WriteString("\n## Available Skills (Slash Commands)\n\n")
		b.WriteString("The agents in this workflow have access to the following slash commands. ")
		b.WriteString("Reference these in the spec where appropriate (e.g. tell the engineer to run /qa after implementation, or the reviewer to use /review).\n\n")
		for _, cmd := range skillCommands {
			b.WriteString(fmt.Sprintf("- %s\n", cmd))
		}
		b.WriteString("\n")
	}

	if len(roles) > 0 {
		b.WriteString("\n## Team Composition\n\n")
		b.WriteString("The following agent roles are active in this workflow:\n")
		for _, r := range roles {
			b.WriteString(fmt.Sprintf("- %s\n", r))
		}
		b.WriteString("\nOnly reference agents that are listed above. Do not include instructions about communicating with agents that are not part of this workflow.\n")
	}

	b.WriteString(fmt.Sprintf("\n## Generate the %s spec\n", role))

	if planContent != "" {
		b.WriteString(fmt.Sprintf("Slice the plan document above into an actionable %s spec. ", role))
		b.WriteString("The plan has already been reviewed and decisions are resolved — do not second-guess scope or architecture. ")
		b.WriteString("Focus on execution: exact file paths, implementation order, verification commands between phases, and test coverage. ")
	} else {
		b.WriteString(fmt.Sprintf("Generate a complete, actionable %s spec for the task above based on this codebase analysis. ", role))
	}

	b.WriteString("Reference actual file paths from the analysis. Use the correct build/test/lint commands for this stack. ")
	if len(skillCommands) > 0 {
		b.WriteString("Integrate the available slash commands into the workflow steps where they add value. ")
	}
	b.WriteString("Output only the markdown spec content — no preamble, no wrapping code fences.\n")

	return b.String()
}
