package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SelectionItem represents a single item in a selection list.
type SelectionItem struct {
	// Label is the primary text (e.g., branch name).
	Label string
	// Description is secondary text (e.g., worktree path).
	Description string
	// Value is the data returned on selection (e.g., path).
	Value string
}

// SelectionOverlay is a scrollable list picker overlay.
type SelectionOverlay struct {
	title        string
	items        []SelectionItem
	selectedIdx  int
	scrollOffset int
	maxVisible   int
	width        int
	footerText   string

	// Dismissed is true when the overlay has been closed.
	Dismissed bool
	// Selected is true when the user pressed enter to select an item.
	Selected bool
}

// NewSelectionOverlay creates a new selection overlay.
func NewSelectionOverlay(title string, items []SelectionItem, footerText string) *SelectionOverlay {
	return &SelectionOverlay{
		title:      title,
		items:      items,
		maxVisible: 8,
		width:      50,
		footerText: footerText,
	}
}

// SetWidth sets the width of the overlay.
func (s *SelectionOverlay) SetWidth(width int) {
	s.width = width
}

// GetSelectedItem returns the currently selected item, or nil if no items.
func (s *SelectionOverlay) GetSelectedItem() *SelectionItem {
	if len(s.items) == 0 {
		return nil
	}
	return &s.items[s.selectedIdx]
}

// HandleKeyPress processes a key press and returns true if the overlay should close.
func (s *SelectionOverlay) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k":
		if s.selectedIdx > 0 {
			s.selectedIdx--
			if s.selectedIdx < s.scrollOffset {
				s.scrollOffset = s.selectedIdx
			}
		}
		return false
	case "down", "j":
		if s.selectedIdx < len(s.items)-1 {
			s.selectedIdx++
			if s.selectedIdx >= s.scrollOffset+s.maxVisible {
				s.scrollOffset = s.selectedIdx - s.maxVisible + 1
			}
		}
		return false
	case "enter":
		s.Selected = true
		s.Dismissed = true
		return true
	case "esc":
		s.Dismissed = true
		return true
	default:
		return false
	}
}

// Render renders the selection overlay.
func (s *SelectionOverlay) Render(opts ...WhitespaceOption) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2).
		Width(s.width)

	titleRendered := lipgloss.NewStyle().
		Bold(true).
		Underline(true).
		Foreground(lipgloss.Color("#7D56F4")).
		Render(s.title)

	selectedLabelStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#1a1a1a")).
		Background(lipgloss.Color("#dde4f0"))
	selectedDescStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555")).
		Background(lipgloss.Color("#dde4f0"))
	normalLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF"))
	normalDescStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#777777"))
	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#777777"))

	var lines []string
	lines = append(lines, titleRendered, "")

	// Scroll indicator at top
	if s.scrollOffset > 0 {
		lines = append(lines, dimStyle.Render("  ↑ more"))
	}

	// Visible items
	end := s.scrollOffset + s.maxVisible
	if end > len(s.items) {
		end = len(s.items)
	}
	for i := s.scrollOffset; i < end; i++ {
		item := s.items[i]
		if i == s.selectedIdx {
			label := fmt.Sprintf("  > %s", item.Label)
			lines = append(lines, selectedLabelStyle.Render(label))
			if item.Description != "" {
				desc := fmt.Sprintf("    %s", item.Description)
				lines = append(lines, selectedDescStyle.Render(desc))
			}
		} else {
			label := fmt.Sprintf("    %s", item.Label)
			lines = append(lines, normalLabelStyle.Render(label))
			if item.Description != "" {
				desc := fmt.Sprintf("    %s", item.Description)
				lines = append(lines, normalDescStyle.Render(desc))
			}
		}
		// Add a blank line between items (but not after the last)
		if i < end-1 {
			lines = append(lines, "")
		}
	}

	// Scroll indicator at bottom
	if end < len(s.items) {
		lines = append(lines, dimStyle.Render("  ↓ more"))
	}

	// Footer
	if s.footerText != "" {
		lines = append(lines, "", dimStyle.Render(s.footerText))
	}

	content := strings.Join(lines, "\n")
	return style.Render(content)
}
