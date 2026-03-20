package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Stage represents a task pipeline stage.
type Stage string

const (
	StageInProgress  Stage = "in_progress"
	StagePlanned     Stage = "planned"
	StageApproved    Stage = "approved"
	StagePending     Stage = "pending"
	StageBlocked     Stage = "blocked"
	StageNeedsReview Stage = "needs_review"
)

// StageOrder defines display priority (most active first).
var StageOrder = []Stage{
	StageInProgress, StageBlocked, StageNeedsReview,
	StageApproved, StagePlanned, StagePending,
}

// StageDirName maps a Stage to its filesystem directory name.
var StageDirName = map[Stage]string{
	StageInProgress:  "in_progress",
	StagePlanned:     "planned",
	StageApproved:    "approved",
	StagePending:     "pending",
	StageBlocked:     "blocked",
	StageNeedsReview: "needs-review",
}

// StageDisplayName returns a human-readable label for the stage.
func StageDisplayName(s Stage) string {
	switch s {
	case StageInProgress:
		return "IN PROGRESS"
	case StagePlanned:
		return "PLANNED"
	case StageApproved:
		return "APPROVED"
	case StagePending:
		return "PENDING"
	case StageBlocked:
		return "BLOCKED"
	case StageNeedsReview:
		return "NEEDS REVIEW"
	default:
		return strings.ToUpper(string(s))
	}
}

// SessionEntry represents a session from sessions.json.
type SessionEntry struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	Workstream    string            `json:"workstream"`
	Branch        string            `json:"branch"`
	Phase         string            `json:"phase"`
	PR            string            `json:"pr"`
	LastHeartbeat string            `json:"last_heartbeat"`
	Summary       string            `json:"summary"`
	NextSteps     []string          `json:"next_steps"`
	Context       SessionContext    `json:"context"`
}

// SessionContext holds context metadata from a session entry.
type SessionContext struct {
	TaskFilesTouched []string `json:"task_files_touched"`
}

// sessionsFile is the top-level JSON structure of sessions.json.
type sessionsFile struct {
	Sessions []SessionEntry `json:"sessions"`
}

// InstanceInfo is a minimal representation of a CS instance, used to avoid
// importing the session package (which would create circular deps).
type InstanceInfo struct {
	Title       string
	Branch      string
	Status      string
	DiffAdded   int
	DiffRemoved int
}

// Item represents a single pipeline task with its matched agent info.
type Item struct {
	Filename       string // e.g. "awnav-4990-preserve-archive-ts.md"
	Stage          Stage
	JiraKey        string        // e.g. "AWNAV-4990"
	Branch         string        // from task file content
	Session        *SessionEntry // matched session (nil if none)
	InstanceIdx    int           // index into CS instance list (-1 if no match)
	InstanceName   string        // CS instance title (empty if no match)
	InstanceStatus string        // "Running"/"Ready"/"Paused" (empty if no match)
	DiffAdded      int
	DiffRemoved    int
}

// Load reads task files, sessions.json, and matches them to CS instances.
// tasksDir is the path to the tasks directory (e.g. ~/agent-mia/tasks).
// sessionsPath is the path to sessions.json.
// instances is the list of currently running CS instances.
func Load(tasksDir string, sessionsPath string, instances []InstanceInfo) ([]Item, error) {
	sessions, err := loadSessions(sessionsPath)
	if err != nil {
		// Non-fatal: sessions.json might not exist yet
		sessions = nil
	}

	// Build branch -> session lookup
	sessionByBranch := make(map[string]*SessionEntry)
	for i := range sessions {
		s := &sessions[i]
		if s.Branch != "" {
			sessionByBranch[s.Branch] = s
		}
	}

	// Build branch -> instance index lookup
	instanceByBranch := make(map[string]int)
	for i, inst := range instances {
		if inst.Branch != "" {
			instanceByBranch[inst.Branch] = i
		}
	}

	var items []Item

	for _, stage := range StageOrder {
		dirName, ok := StageDirName[stage]
		if !ok {
			continue
		}
		stageDir := filepath.Join(tasksDir, dirName)

		entries, err := os.ReadDir(stageDir)
		if err != nil {
			// Directory might not exist — skip
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}

			filePath := filepath.Join(stageDir, entry.Name())
			jiraKey, branch := parseTaskFile(filePath)

			item := Item{
				Filename:    entry.Name(),
				Stage:       stage,
				JiraKey:     jiraKey,
				Branch:      branch,
				InstanceIdx: -1,
			}

			// Match by branch
			if branch != "" {
				if sess, ok := sessionByBranch[branch]; ok {
					item.Session = sess
				}
				if idx, ok := instanceByBranch[branch]; ok {
					item.InstanceIdx = idx
					item.InstanceName = instances[idx].Title
					item.InstanceStatus = instances[idx].Status
					item.DiffAdded = instances[idx].DiffAdded
					item.DiffRemoved = instances[idx].DiffRemoved
				}
			}

			// Fallback: check sessions' task_files_touched for a path match
			if item.Session == nil {
				for i := range sessions {
					s := &sessions[i]
					for _, touched := range s.Context.TaskFilesTouched {
						if strings.HasSuffix(touched, entry.Name()) {
							item.Session = s
							// Also try to match instance via session branch
							if s.Branch != "" {
								if idx, ok := instanceByBranch[s.Branch]; ok {
									item.InstanceIdx = idx
									item.InstanceName = instances[idx].Title
									item.InstanceStatus = instances[idx].Status
									item.DiffAdded = instances[idx].DiffAdded
									item.DiffRemoved = instances[idx].DiffRemoved
								}
							}
							break
						}
					}
					if item.Session != nil {
						break
					}
				}
			}

			items = append(items, item)
		}
	}

	return items, nil
}

// parseTaskFile reads the first 30 lines of a task file and extracts
// Branch and Jira key fields from the YAML-like header or content.
func parseTaskFile(path string) (jiraKey, branch string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineCount := 0
	for scanner.Scan() && lineCount < 30 {
		line := scanner.Text()
		lineCount++

		trimmed := strings.TrimSpace(line)

		// Look for Branch: field (with > prefix for blockquote style)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "> **branch**:") || strings.HasPrefix(lower, "branch:") {
			branch = extractFieldValue(trimmed)
		}
		if strings.HasPrefix(lower, "> **jira**:") || strings.HasPrefix(lower, "jira:") {
			jiraKey = extractFieldValue(trimmed)
		}

		// Try to extract JIRA key from filename-style references like "AWNAV-4990"
		if jiraKey == "" {
			jiraKey = extractJiraKey(trimmed)
		}
	}

	// If no branch found from content, try deriving JIRA key from filename
	if jiraKey == "" {
		base := filepath.Base(path)
		jiraKey = extractJiraKey(base)
	}

	return jiraKey, branch
}

// extractFieldValue extracts the value after the last colon in a field line,
// handling both "Branch: value" and "> **Branch**: value" formats.
func extractFieldValue(line string) string {
	// Remove leading > and ** markers
	cleaned := strings.TrimPrefix(line, ">")
	cleaned = strings.TrimSpace(cleaned)

	// Find the last occurrence of **: or : that separates key from value
	idx := strings.Index(cleaned, "**:")
	if idx >= 0 {
		return strings.TrimSpace(cleaned[idx+3:])
	}
	idx = strings.Index(cleaned, ":")
	if idx >= 0 {
		return strings.TrimSpace(cleaned[idx+1:])
	}
	return ""
}

// extractJiraKey finds a JIRA-style key (e.g. "AWNAV-4990") in a string.
func extractJiraKey(s string) string {
	// Simple scanner: find uppercase letters followed by dash and digits
	words := strings.Fields(s)
	for _, word := range words {
		// Strip common punctuation
		word = strings.Trim(word, "()[]{}:,;\"'`#*")
		if isJiraKey(word) {
			return word
		}
	}

	// Also try the filename without extension
	base := strings.TrimSuffix(s, filepath.Ext(s))
	parts := strings.Split(base, "-")
	if len(parts) >= 2 {
		candidate := strings.ToUpper(parts[0]) + "-" + parts[1]
		if isJiraKey(candidate) {
			return candidate
		}
	}

	return ""
}

// isJiraKey returns true if the string looks like a JIRA key (e.g. "AWNAV-4990").
func isJiraKey(s string) bool {
	dashIdx := strings.Index(s, "-")
	if dashIdx <= 0 || dashIdx >= len(s)-1 {
		return false
	}
	prefix := s[:dashIdx]
	suffix := s[dashIdx+1:]

	// Prefix must be all uppercase letters
	for _, c := range prefix {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	// Suffix must be all digits
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(prefix) >= 2 && len(suffix) >= 1
}

// loadSessions reads and parses sessions.json.
func loadSessions(path string) ([]SessionEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read sessions file: %w", err)
	}

	var sf sessionsFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("failed to parse sessions file: %w", err)
	}

	return sf.Sessions, nil
}
