package git

import (
	"claude-squad/config"
	"claude-squad/log"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func getWorktreeDirectory() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(configDir, "worktrees"), nil
}

// GitWorktree manages git worktree operations for a session
type GitWorktree struct {
	// Path to the repository
	repoPath string
	// Path to the worktree
	worktreePath string
	// Name of the session
	sessionName string
	// Branch name for the worktree
	branchName string
	// Base commit hash for the worktree
	baseCommitSHA string
}

func NewGitWorktreeFromStorage(repoPath string, worktreePath string, sessionName string, branchName string, baseCommitSHA string) *GitWorktree {
	return &GitWorktree{
		repoPath:      repoPath,
		worktreePath:  worktreePath,
		sessionName:   sessionName,
		branchName:    branchName,
		baseCommitSHA: baseCommitSHA,
	}
}

// NewGitWorktree creates a new GitWorktree instance
func NewGitWorktree(repoPath string, sessionName string) (tree *GitWorktree, branchname string, err error) {
	cfg := config.LoadConfig()
	branchName := fmt.Sprintf("%s%s", cfg.BranchPrefix, sessionName)
	// Sanitize the final branch name to handle invalid characters from any source
	// (e.g., backslashes from Windows domain usernames like DOMAIN\user)
	branchName = sanitizeBranchName(branchName)

	// Convert repoPath to absolute path
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		log.ErrorLog.Printf("git worktree path abs error, falling back to repoPath %s: %s", repoPath, err)
		// If we can't get absolute path, use original path as fallback
		absPath = repoPath
	}

	repoPath, err = findGitRepoRoot(absPath)
	if err != nil {
		return nil, "", err
	}

	worktreeDir, err := getWorktreeDirectory()
	if err != nil {
		return nil, "", err
	}

	// Use sanitized branch name for the worktree directory name
	worktreePath := filepath.Join(worktreeDir, branchName)
	worktreePath = worktreePath + "_" + fmt.Sprintf("%x", time.Now().UnixNano())

	return &GitWorktree{
		repoPath:     repoPath,
		sessionName:  sessionName,
		branchName:   branchName,
		worktreePath: worktreePath,
	}, branchName, nil
}

// GetWorktreePath returns the path to the worktree
func (g *GitWorktree) GetWorktreePath() string {
	return g.worktreePath
}

// GetBranchName returns the name of the branch associated with this worktree
func (g *GitWorktree) GetBranchName() string {
	return g.branchName
}

// GetRepoPath returns the path to the repository
func (g *GitWorktree) GetRepoPath() string {
	return g.repoPath
}

// GetRepoName returns the name of the repository (last part of the repoPath).
func (g *GitWorktree) GetRepoName() string {
	return filepath.Base(g.repoPath)
}

// GetBaseCommitSHA returns the base commit SHA for the worktree
func (g *GitWorktree) GetBaseCommitSHA() string {
	return g.baseCommitSHA
}

// runGitCommandStatic runs a git command at the given path without needing a GitWorktree receiver.
// This is used during construction of GitWorktree from an existing worktree.
func runGitCommandStatic(path string, args ...string) (string, error) {
	baseArgs := []string{"-C", path}
	cmd := exec.Command("git", append(baseArgs, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git command failed: %s (%w)", output, err)
	}
	return string(output), nil
}

// NewGitWorktreeFromExisting wraps an already-existing worktree directory.
// It auto-detects the branch and main repo path from the worktree.
func NewGitWorktreeFromExisting(worktreePath string) (*GitWorktree, string, error) {
	absPath, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Detect current branch
	branchOutput, err := runGitCommandStatic(absPath, "branch", "--show-current")
	if err != nil {
		return nil, "", fmt.Errorf("failed to detect branch at %s: %w", absPath, err)
	}
	branchName := strings.TrimSpace(branchOutput)
	if branchName == "" {
		return nil, "", fmt.Errorf("worktree at %s is in detached HEAD state; a branch is required", absPath)
	}

	// Detect main repo path via git rev-parse --git-common-dir
	commonDirOutput, err := runGitCommandStatic(absPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, "", fmt.Errorf("failed to detect main repo from %s: %w", absPath, err)
	}
	commonDir := strings.TrimSpace(commonDirOutput)
	// The common dir is the .git directory of the main repo. Resolve to the repo root.
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(absPath, commonDir)
	}
	repoPath := filepath.Dir(commonDir)
	// Clean the path to resolve any ".." components
	repoPath = filepath.Clean(repoPath)

	// Get base commit SHA (HEAD of the worktree)
	headOutput, err := runGitCommandStatic(absPath, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("failed to get HEAD at %s: %w", absPath, err)
	}
	baseCommitSHA := strings.TrimSpace(headOutput)

	return &GitWorktree{
		repoPath:      repoPath,
		worktreePath:  absPath,
		sessionName:   branchName,
		branchName:    branchName,
		baseCommitSHA: baseCommitSHA,
	}, branchName, nil
}

// IsWorktree checks whether the given path is a git worktree (as opposed to the main repo).
func IsWorktree(path string) bool {
	output, err := runGitCommandStatic(path, "rev-parse", "--git-dir")
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(output)
	// A worktree's --git-dir points to a subdirectory under the main repo's .git/worktrees/
	return strings.Contains(gitDir, "worktrees")
}
