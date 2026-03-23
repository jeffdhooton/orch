package generate

import "strings"

func buildReviewerSystemPrompt(roles []string) string {
	hasPM := false
	for _, r := range roles {
		if r == "pm" {
			hasPM = true
			break
		}
	}

	openingLine := `"You are a code reviewer. Wait for the builder to notify you that work is ready."`
	if hasPM {
		openingLine = `"You are a code reviewer. Wait for the PM or builder to notify you that work is ready."`
	}

	completionStep := "4. **If code looks good:** Notify the builder via .orch-send-builder that the review is complete and the code is approved."
	if hasPM {
		completionStep = "4. **If code looks good:** Notify the PM via .orch-send-pm that the review is complete."
	}

	var b strings.Builder
	b.WriteString(`You are generating a code reviewer spec for a multi-agent workflow managed by orch.

The spec will be read by an autonomous Claude Code agent whose job is to review code when notified.

## Format

Write the spec as plain markdown. Structure it as:

1. **Opening line** — `)
	b.WriteString(openingLine)
	b.WriteString(`

2. **When you receive a review request** — Numbered steps:
   - Read all source files that were created or modified
   - Run the stack's test command (with verbose flag) and lint/vet command
   - Check for specific issues relevant to the task (security, error handling, resource leaks, test coverage, etc.)

3. **Feedback format:**
   - Send feedback to the builder via .orch-send-builder
   - Cite file names and line numbers
   - Categorize every issue as one of:
     - MUST FIX — Blockers (bugs, security issues, missing error handling)
     - SHOULD FIX — Improvements (better patterns, readability, performance)
     - NIT — Style and polish (naming, formatting, minor suggestions)

`)
	b.WriteString(completionStep)
	b.WriteString(`

5. **Follow-up:** Schedule a check to verify the builder addressed feedback using .orch-schedule.

6. **Rules section:**
   - Be specific — cite exact functions, lines, and what to change.
   - Don't rewrite code yourself. Give clear instructions.
   - Failing tests are a blocker. Flag immediately.

## Rules

- Use the correct test/build/lint commands for the detected tech stack.
- The review checklist should be tailored to the specific task (e.g., SQL injection checks for database work, accessibility for frontend work).
- Output only the spec markdown. No preamble.`)

	return b.String()
}
