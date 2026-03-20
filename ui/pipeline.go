package ui

import (
	"claude-squad/pipeline"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

var (
	pipelineStageStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#874BFD", Dark: "#7D56F4"})

	pipelineItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#dddddd"})

	pipelineSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#dde4f0")).
				Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#1a1a1a"})

	pipelineDimStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#999999", Dark: "#666666"})

	pipelineLinkedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#22c55e"))

	pipelineBulletActive   = "●"
	pipelineBulletInactive = "○"
)

// PipelinePane displays pipeline tasks grouped by stage with agent linkage info.
type PipelinePane struct {
	viewport viewport.Model
	items    []pipeline.Item
	cursor   int
	width    int
	height   int
}

// NewPipelinePane creates a new PipelinePane.
func NewPipelinePane() *PipelinePane {
	return &PipelinePane{
		viewport: viewport.New(0, 0),
		cursor:   0,
	}
}

// SetSize sets the viewport dimensions.
func (p *PipelinePane) SetSize(width, height int) {
	p.width = width
	p.height = height
	p.viewport.Width = width
	p.viewport.Height = height
	p.renderContent()
}

// SetItems updates the pipeline data and re-renders.
func (p *PipelinePane) SetItems(items []pipeline.Item) {
	p.items = items
	// Clamp cursor
	if p.cursor >= len(p.items) {
		p.cursor = max(0, len(p.items)-1)
	}
	p.renderContent()
}

// CursorUp moves the highlighted item up.
func (p *PipelinePane) CursorUp() {
	if p.cursor > 0 {
		p.cursor--
		p.renderContent()
		p.ensureCursorVisible()
	}
}

// CursorDown moves the highlighted item down.
func (p *PipelinePane) CursorDown() {
	if p.cursor < len(p.items)-1 {
		p.cursor++
		p.renderContent()
		p.ensureCursorVisible()
	}
}

// GetSelectedItem returns the currently highlighted item, or nil if no items.
func (p *PipelinePane) GetSelectedItem() *pipeline.Item {
	if len(p.items) == 0 || p.cursor < 0 || p.cursor >= len(p.items) {
		return nil
	}
	return &p.items[p.cursor]
}

// String renders the viewport.
func (p *PipelinePane) String() string {
	if len(p.items) == 0 {
		return lipgloss.Place(
			p.width,
			p.height,
			lipgloss.Center,
			lipgloss.Center,
			pipelineDimStyle.Render("No pipeline tasks"),
		)
	}
	return p.viewport.View()
}

// renderContent builds the styled text content and sets it on the viewport.
func (p *PipelinePane) renderContent() {
	if len(p.items) == 0 {
		return
	}

	var b strings.Builder
	currentStage := pipeline.Stage("")

	for i, item := range p.items {
		// Stage header
		if item.Stage != currentStage {
			if currentStage != "" {
				b.WriteString("\n")
			}
			currentStage = item.Stage
			header := "  " + pipelineStageStyle.Render(pipeline.StageDisplayName(item.Stage))
			b.WriteString(header + "\n")
		}

		isSelected := i == p.cursor

		// Build the item line
		bullet := pipelineBulletInactive
		if item.Stage == pipeline.StageInProgress || item.Stage == pipeline.StageBlocked {
			bullet = pipelineBulletActive
		}

		// First line: bullet + JIRA key + task name
		taskName := strings.TrimSuffix(item.Filename, ".md")
		label := fmt.Sprintf("  %s %s", bullet, taskName)
		if item.JiraKey != "" {
			label = fmt.Sprintf("  %s %s %s", bullet, item.JiraKey, stripJiraPrefix(taskName, item.JiraKey))
		}

		// Second line: agent linkage info
		var agentInfo string
		if item.InstanceIdx >= 0 {
			diffInfo := ""
			if item.DiffAdded > 0 || item.DiffRemoved > 0 {
				added := AdditionStyle.Render(fmt.Sprintf("+%d", item.DiffAdded))
				removed := DeletionStyle.Render(fmt.Sprintf("-%d", item.DiffRemoved))
				diffInfo = fmt.Sprintf(" %s/%s", added, removed)
			}
			statusStr := item.InstanceStatus
			agentInfo = fmt.Sprintf("    %s → %s (%s)%s",
				pipelineDimStyle.Render(item.Branch),
				pipelineLinkedStyle.Render(item.InstanceName),
				statusStr,
				diffInfo,
			)
		} else if item.Branch != "" {
			agentInfo = fmt.Sprintf("    %s → %s",
				pipelineDimStyle.Render(item.Branch),
				pipelineDimStyle.Render("(no agent)"),
			)
		} else {
			agentInfo = "    " + pipelineDimStyle.Render("(no branch)")
		}

		if isSelected {
			// Apply highlight to both lines
			labelWidth := lipgloss.Width(label)
			padded := label + strings.Repeat(" ", max(0, p.width-labelWidth))
			b.WriteString(pipelineSelectedStyle.Render(padded) + "\n")

			agentWidth := lipgloss.Width(agentInfo)
			paddedAgent := agentInfo + strings.Repeat(" ", max(0, p.width-agentWidth))
			b.WriteString(pipelineSelectedStyle.Render(paddedAgent) + "\n")
		} else {
			b.WriteString(pipelineItemStyle.Render(label) + "\n")
			b.WriteString(agentInfo + "\n")
		}
	}

	p.viewport.SetContent(b.String())
}

// ensureCursorVisible scrolls the viewport to keep the cursor in view.
func (p *PipelinePane) ensureCursorVisible() {
	// Each item takes 2 lines, plus stage headers take 1-2 lines.
	// Approximate the line of the cursor.
	targetLine := p.estimateCursorLine()
	viewStart := p.viewport.YOffset
	viewEnd := viewStart + p.viewport.Height

	if targetLine < viewStart {
		p.viewport.SetYOffset(targetLine)
	} else if targetLine+2 > viewEnd {
		p.viewport.SetYOffset(targetLine + 2 - p.viewport.Height)
	}
}

// estimateCursorLine estimates which line in the rendered content corresponds
// to the cursor position.
func (p *PipelinePane) estimateCursorLine() int {
	line := 0
	currentStage := pipeline.Stage("")
	for i, item := range p.items {
		if item.Stage != currentStage {
			if currentStage != "" {
				line++ // blank line between stages
			}
			currentStage = item.Stage
			line++ // stage header
		}
		if i == p.cursor {
			return line
		}
		line += 2 // each item is 2 lines (label + agent info)
	}
	return line
}

// stripJiraPrefix removes the JIRA key prefix from a task name for cleaner display.
// e.g. "awnav-4990-preserve-archive-ts" with key "AWNAV-4990" -> "preserve-archive-ts"
func stripJiraPrefix(name, jiraKey string) string {
	if jiraKey == "" {
		return name
	}
	// Try to strip lowercase jira prefix (e.g. "awnav-4990-" from the name)
	lowerKey := strings.ToLower(jiraKey)
	lowerName := strings.ToLower(name)

	if strings.HasPrefix(lowerName, lowerKey+"-") {
		return name[len(lowerKey)+1:]
	}
	if strings.HasPrefix(lowerName, lowerKey) {
		return name[len(lowerKey):]
	}
	return name
}

