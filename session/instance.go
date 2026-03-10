package session

import (
	"claude-squad/log"
	"claude-squad/session/git"
	"claude-squad/session/tmux"
	"path/filepath"

	"fmt"
	"os"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

type Status int

const (
	// Running is the status when the instance is running and claude is working.
	Running Status = iota
	// Ready is if the claude instance is ready to be interacted with (waiting for user input).
	Ready
	// Loading is if the instance is loading (if we are starting it up or something).
	Loading
	// Paused is if the instance is paused (worktree removed but branch preserved).
	Paused
)

// WorktreeMode describes how the instance relates to a git worktree.
type WorktreeMode int

const (
	// WorktreeNew means CS creates and owns the worktree (current default behavior).
	WorktreeNew WorktreeMode = iota
	// WorktreeExisting means the user's existing worktree is used; CS won't delete it.
	WorktreeExisting
	// WorktreeNone means no worktree; the instance runs in the repo cwd.
	WorktreeNone
)

// Instance is a running instance of claude code.
type Instance struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Branch is the branch of the instance.
	Branch string
	// Status is the status of the instance.
	Status Status
	// Program is the program to run in the instance.
	Program string
	// Height is the height of the instance.
	Height int
	// Width is the width of the instance.
	Width int
	// CreatedAt is the time the instance was created.
	CreatedAt time.Time
	// UpdatedAt is the time the instance was last updated.
	UpdatedAt time.Time
	// AutoYes is true if the instance should automatically press enter when prompted.
	AutoYes bool
	// Prompt is the initial prompt to pass to the instance on startup
	Prompt string
	// WorktreeMode describes how this instance relates to a git worktree.
	WorktreeMode WorktreeMode

	// DiffStats stores the current git diff statistics
	diffStats *git.DiffStats

	// The below fields are initialized upon calling Start().

	started bool
	// tmuxSession is the tmux session for the instance.
	tmuxSession *tmux.TmuxSession
	// gitWorktree is the git worktree for the instance.
	gitWorktree *git.GitWorktree
}

// ToInstanceData converts an Instance to its serializable form
func (i *Instance) ToInstanceData() InstanceData {
	data := InstanceData{
		Title:        i.Title,
		Path:         i.Path,
		Branch:       i.Branch,
		Status:       i.Status,
		Height:       i.Height,
		Width:        i.Width,
		CreatedAt:    i.CreatedAt,
		UpdatedAt:    time.Now(),
		Program:      i.Program,
		AutoYes:      i.AutoYes,
		WorktreeMode: int(i.WorktreeMode),
	}

	// Only include worktree data if gitWorktree is initialized
	if i.gitWorktree != nil {
		data.Worktree = GitWorktreeData{
			RepoPath:      i.gitWorktree.GetRepoPath(),
			WorktreePath:  i.gitWorktree.GetWorktreePath(),
			SessionName:   i.Title,
			BranchName:    i.gitWorktree.GetBranchName(),
			BaseCommitSHA: i.gitWorktree.GetBaseCommitSHA(),
		}
	}

	// Only include diff stats if they exist
	if i.diffStats != nil {
		data.DiffStats = DiffStatsData{
			Added:   i.diffStats.Added,
			Removed: i.diffStats.Removed,
			Content: i.diffStats.Content,
		}
	}

	return data
}

// FromInstanceData creates a new Instance from serialized data
func FromInstanceData(data InstanceData) (*Instance, error) {
	mode := WorktreeMode(data.WorktreeMode)

	instance := &Instance{
		Title:        data.Title,
		Path:         data.Path,
		Branch:       data.Branch,
		Status:       data.Status,
		Height:       data.Height,
		Width:        data.Width,
		CreatedAt:    data.CreatedAt,
		UpdatedAt:    data.UpdatedAt,
		Program:      data.Program,
		WorktreeMode: mode,
		diffStats: &git.DiffStats{
			Added:   data.DiffStats.Added,
			Removed: data.DiffStats.Removed,
			Content: data.DiffStats.Content,
		},
	}

	// Only restore gitWorktree for modes that use one.
	if mode != WorktreeNone {
		instance.gitWorktree = git.NewGitWorktreeFromStorage(
			data.Worktree.RepoPath,
			data.Worktree.WorktreePath,
			data.Worktree.SessionName,
			data.Worktree.BranchName,
			data.Worktree.BaseCommitSHA,
		)
	}

	if instance.Paused() {
		instance.started = true
		instance.tmuxSession = tmux.NewTmuxSession(instance.Title, instance.Program)
	} else {
		if err := instance.Start(false); err != nil {
			return nil, err
		}
	}

	return instance, nil
}

// Options for creating a new instance
type InstanceOptions struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Program is the program to run in the instance (e.g. "claude", "aider --model ollama_chat/gemma3:1b")
	Program string
	// If AutoYes is true, then
	AutoYes bool
	// WorktreeMode controls how the instance relates to a git worktree.
	WorktreeMode WorktreeMode
	// ExistingWorktreePath is the path to a pre-existing worktree (only used with WorktreeExisting).
	ExistingWorktreePath string
}

func NewInstance(opts InstanceOptions) (*Instance, error) {
	t := time.Now()

	// Convert path to absolute
	absPath, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	inst := &Instance{
		Title:        opts.Title,
		Status:       Ready,
		Path:         absPath,
		Program:      opts.Program,
		Height:       0,
		Width:        0,
		CreatedAt:    t,
		UpdatedAt:    t,
		AutoYes:      false,
		WorktreeMode: opts.WorktreeMode,
	}

	// For existing worktrees, store the worktree path as the instance path.
	if opts.WorktreeMode == WorktreeExisting && opts.ExistingWorktreePath != "" {
		existingAbs, err := filepath.Abs(opts.ExistingWorktreePath)
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path for existing worktree: %w", err)
		}
		inst.Path = existingAbs
	}

	return inst, nil
}

func (i *Instance) RepoName() (string, error) {
	if !i.started {
		return "", fmt.Errorf("cannot get repo name for instance that has not been started")
	}
	if i.gitWorktree == nil {
		return filepath.Base(i.Path), nil
	}
	return i.gitWorktree.GetRepoName(), nil
}

func (i *Instance) SetStatus(status Status) {
	i.Status = status
}

// firstTimeSetup is true if this is a new instance. Otherwise, it's one loaded from storage.
func (i *Instance) Start(firstTimeSetup bool) error {
	if i.Title == "" {
		return fmt.Errorf("instance title cannot be empty")
	}

	var tmuxSession *tmux.TmuxSession
	if i.tmuxSession != nil {
		// Use existing tmux session (useful for testing)
		tmuxSession = i.tmuxSession
	} else {
		// Create new tmux session
		tmuxSession = tmux.NewTmuxSession(i.Title, i.Program)
	}
	i.tmuxSession = tmuxSession

	if firstTimeSetup {
		switch i.WorktreeMode {
		case WorktreeNew:
			gitWorktree, branchName, err := git.NewGitWorktree(i.Path, i.Title)
			if err != nil {
				return fmt.Errorf("failed to create git worktree: %w", err)
			}
			i.gitWorktree = gitWorktree
			i.Branch = branchName
		case WorktreeExisting:
			gitWorktree, branchName, err := git.NewGitWorktreeFromExisting(i.Path)
			if err != nil {
				return fmt.Errorf("failed to wrap existing worktree: %w", err)
			}
			i.gitWorktree = gitWorktree
			i.Branch = branchName
		case WorktreeNone:
			// No worktree to create; i.Path is used directly as the working directory.
		}
	}

	// Setup error handler to cleanup resources on any error
	var setupErr error
	defer func() {
		if setupErr != nil {
			if cleanupErr := i.Kill(); cleanupErr != nil {
				setupErr = fmt.Errorf("%v (cleanup error: %v)", setupErr, cleanupErr)
			}
		} else {
			i.started = true
		}
	}()

	if !firstTimeSetup {
		// Reuse existing session
		if err := tmuxSession.Restore(); err != nil {
			setupErr = fmt.Errorf("failed to restore existing session: %w", err)
			return setupErr
		}
	} else {
		switch i.WorktreeMode {
		case WorktreeNew:
			// Setup git worktree first
			if err := i.gitWorktree.Setup(); err != nil {
				setupErr = fmt.Errorf("failed to setup git worktree: %w", err)
				return setupErr
			}
			// Create new session in worktree path
			if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
				if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
					err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
				}
				setupErr = fmt.Errorf("failed to start new session: %w", err)
				return setupErr
			}
		case WorktreeExisting:
			// Worktree already exists, just start tmux there
			if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
				setupErr = fmt.Errorf("failed to start new session: %w", err)
				return setupErr
			}
		case WorktreeNone:
			// Start tmux in the instance path directly
			if err := i.tmuxSession.Start(i.Path); err != nil {
				setupErr = fmt.Errorf("failed to start new session: %w", err)
				return setupErr
			}
		}
	}

	i.SetStatus(Running)

	return nil
}

// Kill terminates the instance and cleans up all resources
func (i *Instance) Kill() error {
	if !i.started {
		// If instance was never started, just return success
		return nil
	}

	var errs []error

	// Always try to cleanup tmux session first since it's using the git worktree
	if i.tmuxSession != nil {
		if err := i.tmuxSession.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close tmux session: %w", err))
		}
	}

	// Only clean up git worktree for WorktreeNew (CS-owned) instances.
	// WorktreeExisting: leave the user's worktree alone.
	// WorktreeNone: no worktree to clean up.
	if i.WorktreeMode == WorktreeNew && i.gitWorktree != nil {
		if err := i.gitWorktree.Cleanup(); err != nil {
			errs = append(errs, fmt.Errorf("failed to cleanup git worktree: %w", err))
		}
	}

	return i.combineErrors(errs)
}

// combineErrors combines multiple errors into a single error
func (i *Instance) combineErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple cleanup errors occurred:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return fmt.Errorf("%s", errMsg)
}

func (i *Instance) Preview() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContent()
}

func (i *Instance) HasUpdated() (updated bool, hasPrompt bool) {
	if !i.started {
		return false, false
	}
	return i.tmuxSession.HasUpdated()
}

// TapEnter sends an enter key press to the tmux session if AutoYes is enabled.
// CheckAndHandleTrustPrompt checks for and dismisses the trust prompt for supported programs.
func (i *Instance) CheckAndHandleTrustPrompt() bool {
	if !i.started || i.tmuxSession == nil {
		return false
	}
	program := i.Program
	if !strings.HasSuffix(program, tmux.ProgramClaude) &&
		!strings.HasSuffix(program, tmux.ProgramAider) &&
		!strings.HasSuffix(program, tmux.ProgramGemini) {
		return false
	}
	return i.tmuxSession.CheckAndHandleTrustPrompt()
}

func (i *Instance) TapEnter() {
	if !i.started || !i.AutoYes {
		return
	}
	if err := i.tmuxSession.TapEnter(); err != nil {
		log.ErrorLog.Printf("error tapping enter: %v", err)
	}
}

func (i *Instance) Attach() (chan struct{}, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot attach instance that has not been started")
	}
	return i.tmuxSession.Attach()
}

func (i *Instance) SetPreviewSize(width, height int) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot set preview size for instance that has not been started or " +
			"is paused")
	}
	return i.tmuxSession.SetDetachedSize(width, height)
}

// GetGitWorktree returns the git worktree for the instance
func (i *Instance) GetGitWorktree() (*git.GitWorktree, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot get git worktree for instance that has not been started")
	}
	return i.gitWorktree, nil
}

// GetWorktreePath returns the worktree path for the instance, or empty string if unavailable
func (i *Instance) GetWorktreePath() string {
	if i.gitWorktree == nil {
		return ""
	}
	return i.gitWorktree.GetWorktreePath()
}

func (i *Instance) Started() bool {
	return i.started
}

// SetTitle sets the title of the instance. Returns an error if the instance has started.
// We cant change the title once it's been used for a tmux session etc.
func (i *Instance) SetTitle(title string) error {
	if i.started {
		return fmt.Errorf("cannot change title of a started instance")
	}
	i.Title = title
	return nil
}

func (i *Instance) Paused() bool {
	return i.Status == Paused
}

// TmuxAlive returns true if the tmux session is alive. This is a sanity check before attaching.
func (i *Instance) TmuxAlive() bool {
	return i.tmuxSession.DoesSessionExist()
}

// Pause stops the tmux session and removes the worktree, preserving the branch
func (i *Instance) Pause() error {
	if !i.started {
		return fmt.Errorf("cannot pause instance that has not been started")
	}
	if i.Status == Paused {
		return fmt.Errorf("instance is already paused")
	}

	var errs []error

	switch i.WorktreeMode {
	case WorktreeNew:
		// Commit changes, detach tmux, remove worktree (original behavior)
		if dirty, err := i.gitWorktree.IsDirty(); err != nil {
			errs = append(errs, fmt.Errorf("failed to check if worktree is dirty: %w", err))
			log.ErrorLog.Print(err)
		} else if dirty {
			commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s (paused)", i.Title, time.Now().Format(time.RFC822))
			if err := i.gitWorktree.CommitChanges(commitMsg); err != nil {
				errs = append(errs, fmt.Errorf("failed to commit changes: %w", err))
				log.ErrorLog.Print(err)
				return i.combineErrors(errs)
			}
		}

		if err := i.tmuxSession.DetachSafely(); err != nil {
			errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
			log.ErrorLog.Print(err)
		}

		if _, err := os.Stat(i.gitWorktree.GetWorktreePath()); err == nil {
			if err := i.gitWorktree.Remove(); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove git worktree: %w", err))
				log.ErrorLog.Print(err)
				return i.combineErrors(errs)
			}
			if err := i.gitWorktree.Prune(); err != nil {
				errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
				log.ErrorLog.Print(err)
				return i.combineErrors(errs)
			}
		}

	case WorktreeExisting:
		// Commit changes but do NOT remove the user's worktree
		if dirty, err := i.gitWorktree.IsDirty(); err != nil {
			errs = append(errs, fmt.Errorf("failed to check if worktree is dirty: %w", err))
			log.ErrorLog.Print(err)
		} else if dirty {
			commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s (paused)", i.Title, time.Now().Format(time.RFC822))
			if err := i.gitWorktree.CommitChanges(commitMsg); err != nil {
				errs = append(errs, fmt.Errorf("failed to commit changes: %w", err))
				log.ErrorLog.Print(err)
				return i.combineErrors(errs)
			}
		}

		if err := i.tmuxSession.DetachSafely(); err != nil {
			errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
			log.ErrorLog.Print(err)
		}

	case WorktreeNone:
		// Just detach tmux, no git operations
		if err := i.tmuxSession.DetachSafely(); err != nil {
			errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
			log.ErrorLog.Print(err)
		}
	}

	if err := i.combineErrors(errs); err != nil {
		log.ErrorLog.Print(err)
		return err
	}

	i.SetStatus(Paused)
	if i.gitWorktree != nil {
		_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())
	}
	return nil
}

// Resume recreates the worktree and restarts the tmux session
func (i *Instance) Resume() error {
	if !i.started {
		return fmt.Errorf("cannot resume instance that has not been started")
	}
	if i.Status != Paused {
		return fmt.Errorf("can only resume paused instances")
	}

	// Determine the working directory for the tmux session
	var workDir string

	switch i.WorktreeMode {
	case WorktreeNew:
		// Check if branch is checked out
		if checked, err := i.gitWorktree.IsBranchCheckedOut(); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to check if branch is checked out: %w", err)
		} else if checked {
			return fmt.Errorf("cannot resume: branch is checked out, please switch to a different branch")
		}

		// Recreate the worktree from the branch
		if err := i.gitWorktree.Setup(); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to setup git worktree: %w", err)
		}
		workDir = i.gitWorktree.GetWorktreePath()

	case WorktreeExisting:
		// Worktree already exists on disk, no setup needed
		workDir = i.gitWorktree.GetWorktreePath()

	case WorktreeNone:
		// No worktree, use instance path
		workDir = i.Path
	}

	// Check if tmux session still exists from pause, otherwise create new one
	if i.tmuxSession.DoesSessionExist() {
		if err := i.tmuxSession.Restore(); err != nil {
			log.ErrorLog.Print(err)
			// If restore fails, fall back to creating new session
			if err := i.tmuxSession.Start(workDir); err != nil {
				log.ErrorLog.Print(err)
				if i.WorktreeMode == WorktreeNew && i.gitWorktree != nil {
					if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
						err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
						log.ErrorLog.Print(err)
					}
				}
				return fmt.Errorf("failed to start new session: %w", err)
			}
		}
	} else {
		if err := i.tmuxSession.Start(workDir); err != nil {
			log.ErrorLog.Print(err)
			if i.WorktreeMode == WorktreeNew && i.gitWorktree != nil {
				if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
					err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
					log.ErrorLog.Print(err)
				}
			}
			return fmt.Errorf("failed to start new session: %w", err)
		}
	}

	i.SetStatus(Running)
	return nil
}

// UpdateDiffStats updates the git diff statistics for this instance
func (i *Instance) UpdateDiffStats() error {
	if !i.started || i.gitWorktree == nil {
		i.diffStats = nil
		return nil
	}

	if i.Status == Paused {
		// Keep the previous diff stats if the instance is paused
		return nil
	}

	stats := i.gitWorktree.Diff()
	if stats.Error != nil {
		if strings.Contains(stats.Error.Error(), "base commit SHA not set") {
			// Worktree is not fully set up yet, not an error
			i.diffStats = nil
			return nil
		}
		return fmt.Errorf("failed to get diff stats: %w", stats.Error)
	}

	i.diffStats = stats
	return nil
}

// ComputeDiff runs the expensive git diff I/O and returns the result without
// mutating instance state. Safe to call from a background goroutine.
func (i *Instance) ComputeDiff() *git.DiffStats {
	if !i.started || i.Status == Paused || i.gitWorktree == nil {
		return nil
	}
	return i.gitWorktree.Diff()
}

// SetDiffStats sets the diff statistics on the instance. Should be called from
// the main event loop to avoid data races with View.
func (i *Instance) SetDiffStats(stats *git.DiffStats) {
	i.diffStats = stats
}

// GetDiffStats returns the current git diff statistics
func (i *Instance) GetDiffStats() *git.DiffStats {
	return i.diffStats
}

// SendPrompt sends a prompt to the tmux session
func (i *Instance) SendPrompt(prompt string) error {
	if !i.started {
		return fmt.Errorf("instance not started")
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if err := i.tmuxSession.SendKeys(prompt); err != nil {
		return fmt.Errorf("error sending keys to tmux session: %w", err)
	}

	// Brief pause to prevent carriage return from being interpreted as newline
	time.Sleep(100 * time.Millisecond)
	if err := i.tmuxSession.TapEnter(); err != nil {
		return fmt.Errorf("error tapping enter: %w", err)
	}

	return nil
}

// PreviewFullHistory captures the entire tmux pane output including full scrollback history
func (i *Instance) PreviewFullHistory() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContentWithOptions("-", "-")
}

// SetTmuxSession sets the tmux session for testing purposes
func (i *Instance) SetTmuxSession(session *tmux.TmuxSession) {
	i.tmuxSession = session
}

// SendKeys sends keys to the tmux session
func (i *Instance) SendKeys(keys string) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot send keys to instance that has not been started or is paused")
	}
	return i.tmuxSession.SendKeys(keys)
}
