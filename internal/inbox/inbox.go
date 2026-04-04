package inbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// inboxDirOverride allows tests to redirect inbox writes to a temp directory.
var inboxDirOverride string

// InboxDir returns the path to the gstack global inbox messages directory.
func InboxDir() string {
	if inboxDirOverride != "" {
		return inboxDirOverride
	}
	return filepath.Join(os.Getenv("HOME"), ".gstack", "inbox", "messages")
}

// SendMessage writes a structured markdown message to the gstack global inbox.
// msgType is one of: info, unblock, handoff, question.
// from identifies the sender (e.g. "myproject/main (orch:engineer-1)").
// target is "all" or a project name.
// body is the message content.
func SendMessage(msgType, from, target, body string) error {
	dir := InboxDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating inbox directory: %w", err)
	}

	timestamp := time.Now().Unix()
	filename := fmt.Sprintf("%d-%s.md", timestamp, sanitize(from))

	content := fmt.Sprintf("---\ntype: %s\nfrom: %s\ntarget: %s\ndate: %s\n---\n\n%s\n",
		msgType, from, target,
		time.Now().Format("2006-01-02 15:04"),
		body)

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing inbox message: %w", err)
	}
	return nil
}

// Message represents a parsed inbox message file.
type Message struct {
	Path     string
	Type     string
	From     string
	Target   string
	Date     string
	Body     string
	FileTime int64 // unix timestamp from filename
}

// ReadMessages reads inbox messages, optionally filtered by target and a minimum
// unix timestamp. Messages with target "all" always match. Returns messages sorted
// by file timestamp ascending.
func ReadMessages(target string, since int64) ([]Message, error) {
	dir := InboxDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading inbox directory: %w", err)
	}

	var msgs []Message
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		// Extract timestamp from filename: {timestamp}-{sender}.md
		ts := extractTimestamp(entry.Name())
		if ts <= since {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		msg := parseMessage(string(content))
		msg.Path = path
		msg.FileTime = ts

		// Filter by target: match "all" or exact target match.
		if target != "" && msg.Target != "all" && msg.Target != target {
			continue
		}

		msgs = append(msgs, msg)
	}

	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].FileTime < msgs[j].FileTime
	})
	return msgs, nil
}

// AgentFrom builds the "from" field for an orch agent.
// Format: "project/branch (orch:role-name)" or "project/branch (orch:agent-name)"
func AgentFrom(agentDir, agentName, agentRole string) string {
	project := filepath.Base(agentDir)
	branch := gitBranch(agentDir)
	return fmt.Sprintf("%s/%s (orch:%s)", project, branch, agentName)
}

// IsOrchMessage returns true if the message's from field indicates it came from an orch agent.
func IsOrchMessage(from string) bool {
	return strings.Contains(from, "(orch:")
}

// gitBranch returns the current git branch for a directory, or "main" as fallback.
func gitBranch(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "main"
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "main"
	}
	return branch
}

var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitize makes a string safe for use in filenames.
func sanitize(s string) string {
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// extractTimestamp pulls the unix timestamp prefix from an inbox message filename.
func extractTimestamp(filename string) int64 {
	parts := strings.SplitN(filename, "-", 2)
	if len(parts) == 0 {
		return 0
	}
	var ts int64
	fmt.Sscanf(parts[0], "%d", &ts)
	return ts
}

// parseMessage extracts frontmatter fields and body from an inbox message.
func parseMessage(content string) Message {
	var msg Message

	// Split on frontmatter delimiters.
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		msg.Body = strings.TrimSpace(content)
		return msg
	}

	// Parse YAML-like frontmatter (simple key: value).
	for _, line := range strings.Split(parts[1], "\n") {
		line = strings.TrimSpace(line)
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch key {
		case "type":
			msg.Type = val
		case "from":
			msg.From = val
		case "target":
			msg.Target = val
		case "date":
			msg.Date = val
		}
	}

	msg.Body = strings.TrimSpace(parts[2])
	return msg
}
