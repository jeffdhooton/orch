package analyze

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// CollectGitInfo gathers git state from the target directory.
// Fails gracefully — returns empty fields if git commands fail.
func CollectGitInfo(dir string) GitInfo {
	g := GitInfo{}

	g.Branch = gitCmd(dir, "rev-parse", "--abbrev-ref", "HEAD")
	g.RemoteURL = gitCmd(dir, "remote", "get-url", "origin")
	g.HasUncommitted = gitCmd(dir, "status", "--porcelain") != ""

	logOutput := gitCmd(dir, "log", "--oneline", "-10")
	if logOutput != "" {
		g.RecentCommits = strings.Split(logOutput, "\n")
	}

	return g
}

func gitCmd(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
