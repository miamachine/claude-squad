package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTaskFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("extracts branch and jira from blockquote format", func(t *testing.T) {
		content := `# Fix archive timestamp preservation

> **Jira**: AWNAV-4990
> **Branch**: mia_awnav-4990-preserve-archive-ts
> **Status**: in_progress

Some description here.
`
		path := filepath.Join(dir, "awnav-4990-preserve-archive-ts.md")
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))

		jiraKey, branch := parseTaskFile(path)
		assert.Equal(t, "AWNAV-4990", jiraKey)
		assert.Equal(t, "mia_awnav-4990-preserve-archive-ts", branch)
	})

	t.Run("extracts branch and jira from plain format", func(t *testing.T) {
		content := `# Some task
Jira: PROJ-123
Branch: agent/proj-123-fix
`
		path := filepath.Join(dir, "proj-123-fix.md")
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))

		jiraKey, branch := parseTaskFile(path)
		assert.Equal(t, "PROJ-123", jiraKey)
		assert.Equal(t, "agent/proj-123-fix", branch)
	})

	t.Run("extracts jira from filename when not in content", func(t *testing.T) {
		content := `# Fix notification badge
Some description without explicit jira field.
`
		path := filepath.Join(dir, "awnav-4287-fix-notification-badge.md")
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))

		jiraKey, branch := parseTaskFile(path)
		assert.Equal(t, "AWNAV-4287", jiraKey)
		assert.Equal(t, "", branch)
	})

	t.Run("handles missing file gracefully", func(t *testing.T) {
		jiraKey, branch := parseTaskFile("/nonexistent/file.md")
		assert.Equal(t, "", jiraKey)
		assert.Equal(t, "", branch)
	})
}

func TestIsJiraKey(t *testing.T) {
	assert.True(t, isJiraKey("AWNAV-4990"))
	assert.True(t, isJiraKey("AB-1"))
	assert.True(t, isJiraKey("PROJ-12345"))
	assert.False(t, isJiraKey("a-123"))     // lowercase prefix
	assert.False(t, isJiraKey("AB-"))        // no number
	assert.False(t, isJiraKey("-123"))        // no prefix
	assert.False(t, isJiraKey("ABC"))         // no dash
	assert.False(t, isJiraKey("A-1"))         // prefix too short
}

func TestExtractFieldValue(t *testing.T) {
	assert.Equal(t, "AWNAV-4990", extractFieldValue("> **Jira**: AWNAV-4990"))
	assert.Equal(t, "some-branch", extractFieldValue("Branch: some-branch"))
	assert.Equal(t, "value", extractFieldValue("> **Key**: value"))
	assert.Equal(t, "", extractFieldValue("no colon here"))
}

func TestLoadSessions(t *testing.T) {
	dir := t.TempDir()

	t.Run("loads valid sessions file", func(t *testing.T) {
		sf := sessionsFile{
			Sessions: []SessionEntry{
				{
					ID:     "test-session-1",
					Status: "active",
					Branch: "mia_test-branch",
					Phase:  "implementing",
				},
				{
					ID:     "test-session-2",
					Status: "completed",
					Branch: "mia_other-branch",
				},
			},
		}
		data, err := json.Marshal(sf)
		require.NoError(t, err)

		path := filepath.Join(dir, "sessions.json")
		require.NoError(t, os.WriteFile(path, data, 0644))

		sessions, err := loadSessions(path)
		require.NoError(t, err)
		assert.Len(t, sessions, 2)
		assert.Equal(t, "mia_test-branch", sessions[0].Branch)
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		_, err := loadSessions(filepath.Join(dir, "nonexistent.json"))
		assert.Error(t, err)
	})
}

func TestLoad(t *testing.T) {
	tasksDir := t.TempDir()
	sessionsDir := t.TempDir()

	// Create stage directories
	require.NoError(t, os.MkdirAll(filepath.Join(tasksDir, "in_progress"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tasksDir, "planned"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tasksDir, "blocked"), 0755))

	// Create task files
	task1 := `# Preserve archive timestamp
> **Jira**: AWNAV-4990
> **Branch**: mia_awnav-4990-preserve-archive-ts
`
	task2 := `# Fix notification badge
> **Jira**: AWNAV-4287
`
	task3 := `# Use labo for feed
> **Branch**: mia_awnav-5023-feed-ts
`
	require.NoError(t, os.WriteFile(
		filepath.Join(tasksDir, "in_progress", "awnav-4990-preserve-archive-ts.md"),
		[]byte(task1), 0644))
	require.NoError(t, os.WriteFile(
		filepath.Join(tasksDir, "planned", "awnav-4287-fix-notification-badge.md"),
		[]byte(task2), 0644))
	require.NoError(t, os.WriteFile(
		filepath.Join(tasksDir, "blocked", "awnav-5023-feed-ts.md"),
		[]byte(task3), 0644))

	// Create sessions.json
	sf := sessionsFile{
		Sessions: []SessionEntry{
			{
				ID:     "session-1",
				Status: "active",
				Branch: "mia_awnav-4990-preserve-archive-ts",
				Phase:  "iterating",
			},
		},
	}
	sessionsData, err := json.Marshal(sf)
	require.NoError(t, err)
	sessionsPath := filepath.Join(sessionsDir, "sessions.json")
	require.NoError(t, os.WriteFile(sessionsPath, sessionsData, 0644))

	// Create instances
	instances := []InstanceInfo{
		{
			Title:       "awnav-4990-preserve",
			Branch:      "mia_awnav-4990-preserve-archive-ts",
			Status:      "Running",
			DiffAdded:   12,
			DiffRemoved: 3,
		},
		{
			Title:  "awnav-5023-feed",
			Branch: "mia_awnav-5023-feed-ts",
			Status: "Ready",
		},
	}

	items, err := Load(tasksDir, sessionsPath, instances)
	require.NoError(t, err)

	// Items should be ordered by StageOrder: in_progress, blocked, then planned
	require.Len(t, items, 3)

	// First item: in_progress task (AWNAV-4990), matched to both session and instance
	assert.Equal(t, StageInProgress, items[0].Stage)
	assert.Equal(t, "AWNAV-4990", items[0].JiraKey)
	assert.Equal(t, "mia_awnav-4990-preserve-archive-ts", items[0].Branch)
	assert.NotNil(t, items[0].Session)
	assert.Equal(t, "session-1", items[0].Session.ID)
	assert.Equal(t, 0, items[0].InstanceIdx)
	assert.Equal(t, "awnav-4990-preserve", items[0].InstanceName)
	assert.Equal(t, "Running", items[0].InstanceStatus)
	assert.Equal(t, 12, items[0].DiffAdded)
	assert.Equal(t, 3, items[0].DiffRemoved)

	// Second item: blocked task (AWNAV-5023), matched to instance but no session
	assert.Equal(t, StageBlocked, items[1].Stage)
	assert.Equal(t, "mia_awnav-5023-feed-ts", items[1].Branch)
	assert.Nil(t, items[1].Session)
	assert.Equal(t, 1, items[1].InstanceIdx)
	assert.Equal(t, "awnav-5023-feed", items[1].InstanceName)

	// Third item: planned task (AWNAV-4287), no branch, no match
	assert.Equal(t, StagePlanned, items[2].Stage)
	assert.Equal(t, "AWNAV-4287", items[2].JiraKey)
	assert.Equal(t, "", items[2].Branch)
	assert.Nil(t, items[2].Session)
	assert.Equal(t, -1, items[2].InstanceIdx)
}

func TestLoadWithTaskFilesTouched(t *testing.T) {
	tasksDir := t.TempDir()
	sessionsDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tasksDir, "in_progress"), 0755))

	task := `# Some task without branch field
> **Jira**: PROJ-100
`
	require.NoError(t, os.WriteFile(
		filepath.Join(tasksDir, "in_progress", "proj-100-task.md"),
		[]byte(task), 0644))

	// Session references the task file via context.task_files_touched
	sf := sessionsFile{
		Sessions: []SessionEntry{
			{
				ID:     "session-x",
				Status: "active",
				Branch: "mia_proj-100-fix",
				Context: SessionContext{
					TaskFilesTouched: []string{
						"/some/path/tasks/in_progress/proj-100-task.md",
					},
				},
			},
		},
	}
	sessionsData, err := json.Marshal(sf)
	require.NoError(t, err)
	sessionsPath := filepath.Join(sessionsDir, "sessions.json")
	require.NoError(t, os.WriteFile(sessionsPath, sessionsData, 0644))

	instances := []InstanceInfo{
		{
			Title:  "proj-100-fix",
			Branch: "mia_proj-100-fix",
			Status: "Running",
		},
	}

	items, err := Load(tasksDir, sessionsPath, instances)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// Should be matched via task_files_touched fallback
	assert.NotNil(t, items[0].Session)
	assert.Equal(t, "session-x", items[0].Session.ID)
	assert.Equal(t, 0, items[0].InstanceIdx)
	assert.Equal(t, "proj-100-fix", items[0].InstanceName)
}

func TestLoadMissingSessions(t *testing.T) {
	tasksDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tasksDir, "in_progress"), 0755))

	task := `# Simple task
> **Jira**: TEST-1
`
	require.NoError(t, os.WriteFile(
		filepath.Join(tasksDir, "in_progress", "test-1-task.md"),
		[]byte(task), 0644))

	// Non-existent sessions file should not cause an error
	items, err := Load(tasksDir, "/nonexistent/sessions.json", nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "TEST-1", items[0].JiraKey)
	assert.Nil(t, items[0].Session)
	assert.Equal(t, -1, items[0].InstanceIdx)
}

func TestLoadEmptyTasksDir(t *testing.T) {
	tasksDir := t.TempDir()
	// No stage directories created

	items, err := Load(tasksDir, "/nonexistent/sessions.json", nil)
	require.NoError(t, err)
	assert.Empty(t, items)
}
