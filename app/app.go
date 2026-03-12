package app

import (
	"claude-squad/config"
	"claude-squad/keys"
	"claude-squad/log"
	"claude-squad/pipeline"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/ui"
	"claude-squad/ui/overlay"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const GlobalInstanceLimit = 10

// Run is the main entrypoint into the application.
func Run(ctx context.Context, program string, autoYes bool) error {
	p := tea.NewProgram(
		newHome(ctx, program, autoYes),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(), // Mouse scroll
	)
	_, err := p.Run()
	return err
}

type state int

const (
	stateDefault state = iota
	// stateNew is the state when the user is creating a new instance.
	stateNew
	// statePrompt is the state when the user is entering a prompt.
	statePrompt
	// stateHelp is the state when a help screen is displayed.
	stateHelp
	// stateConfirm is the state when a confirmation modal is displayed.
	stateConfirm
	// stateNewMenu is the state when the user is choosing a new-instance mode (n key submenu).
	stateNewMenu
	// stateRepoSelect is the state when the user is picking a repo for a new worktree.
	stateRepoSelect
	// stateAttachList is the state when the user is picking from a list of existing worktrees.
	stateAttachList
	// stateAttachPath is the state when the user is entering a worktree path.
	stateAttachPath
	// stateDirSelect is the state when the user is picking a directory for no-worktree mode.
	stateDirSelect
	// stateDirPath is the state when the user is entering a directory path manually.
	stateDirPath
)

type home struct {
	ctx context.Context

	// -- Storage and Configuration --

	program string
	autoYes bool

	// storage is the interface for saving/loading data to/from the app's state
	storage *session.Storage
	// appConfig stores persistent application configuration
	appConfig *config.Config
	// appState stores persistent application state like seen help screens
	appState config.AppState

	// -- State --

	// state is the current discrete state of the application
	state state
	// newInstanceFinalizer is called when the state is stateNew and then you press enter.
	// It registers the new instance in the list after the instance has been started.
	newInstanceFinalizer func()

	// promptAfterName tracks if we should enter prompt mode after naming
	promptAfterName bool

	// keySent is used to manage underlining menu items
	keySent bool

	// -- UI Components --

	// list displays the list of instances
	list *ui.List
	// menu displays the bottom menu
	menu *ui.Menu
	// tabbedWindow displays the tabbed window with preview and diff panes
	tabbedWindow *ui.TabbedWindow
	// errBox displays error messages
	errBox *ui.ErrBox
	// global spinner instance. we plumb this down to where it's needed
	spinner spinner.Model
	// textInputOverlay handles text input with state
	textInputOverlay *overlay.TextInputOverlay
	// textOverlay displays text information
	textOverlay *overlay.TextOverlay
	// confirmationOverlay displays confirmation modals
	confirmationOverlay *overlay.ConfirmationOverlay
	// selectionOverlay displays a scrollable list picker
	selectionOverlay *overlay.SelectionOverlay

	// -- Attach/New mode fields --

	// attachMode is the worktree mode selected during attach flow.
	attachMode session.WorktreeMode
	// attachPath is the worktree path being entered during stateAttachPath.
	attachPath string
	// dirPath is the directory path being entered during stateDirPath.
	dirPath string
	// selectedRepoPath is the repo path selected during stateRepoSelect for new worktree creation.
	selectedRepoPath string

	// -- Pipeline fields --

	// agentDir is the path to the agent directory (e.g. ~/agent-mia)
	agentDir string
}

func newHome(ctx context.Context, program string, autoYes bool) *home {
	// Load application config
	appConfig := config.LoadConfig()

	// Load application state
	appState := config.LoadState()

	// Initialize storage
	storage, err := session.NewStorage(appState)
	if err != nil {
		fmt.Printf("Failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	// Resolve agent directory from env or default to ~/agent-mia
	agentDir := os.Getenv("AGENT_DIR")
	if agentDir == "" {
		home, _ := os.UserHomeDir()
		agentDir = filepath.Join(home, "agent-mia")
	}

	h := &home{
		ctx:          ctx,
		spinner:      spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(), ui.NewPipelinePane()),
		errBox:       ui.NewErrBox(),
		storage:      storage,
		appConfig:    appConfig,
		program:      program,
		autoYes:      autoYes,
		state:        stateDefault,
		appState:     appState,
		agentDir:     agentDir,
	}
	h.list = ui.NewList(&h.spinner, autoYes)

	// Load saved instances
	instances, err := storage.LoadInstances()
	if err != nil {
		fmt.Printf("Failed to load instances: %v\n", err)
		os.Exit(1)
	}

	// Add loaded instances to the list
	for _, instance := range instances {
		// Call the finalizer immediately.
		h.list.AddInstance(instance)()
		if autoYes {
			instance.AutoYes = true
		}
	}

	return h
}

// updateHandleWindowSizeEvent sets the sizes of the components.
// The components will try to render inside their bounds.
func (m *home) updateHandleWindowSizeEvent(msg tea.WindowSizeMsg) {
	// List takes 30% of width, preview takes 70%
	listWidth := int(float32(msg.Width) * 0.3)
	tabsWidth := msg.Width - listWidth

	// Menu takes 10% of height, list and window take 90%
	contentHeight := int(float32(msg.Height) * 0.9)
	menuHeight := msg.Height - contentHeight - 1     // minus 1 for error box
	m.errBox.SetSize(int(float32(msg.Width)*0.9), 1) // error box takes 1 row

	m.tabbedWindow.SetSize(tabsWidth, contentHeight)
	m.list.SetSize(listWidth, contentHeight)

	if m.textInputOverlay != nil {
		m.textInputOverlay.SetSize(int(float32(msg.Width)*0.6), int(float32(msg.Height)*0.4))
	}
	if m.textOverlay != nil {
		m.textOverlay.SetWidth(int(float32(msg.Width) * 0.6))
	}

	previewWidth, previewHeight := m.tabbedWindow.GetPreviewSize()
	if err := m.list.SetSessionPreviewSize(previewWidth, previewHeight); err != nil {
		log.ErrorLog.Print(err)
	}
	m.menu.SetSize(msg.Width, menuHeight)
}

func (m *home) Init() tea.Cmd {
	// Upon starting, we want to start the spinner. Whenever we get a spinner.TickMsg, we
	// update the spinner, which sends a new spinner.TickMsg. I think this lasts forever lol.
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			time.Sleep(100 * time.Millisecond)
			return previewTickMsg{}
		},
		tickUpdateMetadataCmd(m.list.GetInstances()),
		pipelineTick(),
	)
}

func (m *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hideErrMsg:
		m.errBox.Clear()
	case previewTickMsg:
		cmd := m.instanceChanged()
		return m, tea.Batch(
			cmd,
			func() tea.Msg {
				time.Sleep(100 * time.Millisecond)
				return previewTickMsg{}
			},
		)
	case keyupMsg:
		m.menu.ClearKeydown()
		return m, nil
	case metadataUpdateDoneMsg:
		for _, r := range msg.results {
			if r.updated {
				r.instance.SetStatus(session.Running)
			} else if r.hasPrompt {
				r.instance.TapEnter()
			} else {
				r.instance.SetStatus(session.Ready)
			}
			if r.diffStats != nil && r.diffStats.Error != nil {
				if !strings.Contains(r.diffStats.Error.Error(), "base commit SHA not set") {
					log.WarningLog.Printf("could not update diff stats: %v", r.diffStats.Error)
				}
				r.instance.SetDiffStats(nil)
			} else {
				r.instance.SetDiffStats(r.diffStats)
			}
		}
		return m, tickUpdateMetadataCmd(m.list.GetInstances())
	case pipelineTickMsg:
		instances := m.list.GetInstances()
		agentDir := m.agentDir
		return m, func() tea.Msg {
			infos := make([]pipeline.InstanceInfo, len(instances))
			for i, inst := range instances {
				status := "Unknown"
				switch inst.Status {
				case session.Running:
					status = "Running"
				case session.Ready:
					status = "Ready"
				case session.Loading:
					status = "Loading"
				case session.Paused:
					status = "Paused"
				}
				var added, removed int
				if stats := inst.GetDiffStats(); stats != nil && stats.Error == nil {
					added = stats.Added
					removed = stats.Removed
				}
				infos[i] = pipeline.InstanceInfo{
					Title:       inst.Title,
					Branch:      inst.Branch,
					Status:      status,
					DiffAdded:   added,
					DiffRemoved: removed,
				}
			}
			items, err := pipeline.Load(
				filepath.Join(agentDir, "tasks"),
				filepath.Join(agentDir, "sessions.json"),
				infos,
			)
			return pipelineUpdateDoneMsg{items: items, err: err}
		}
	case pipelineUpdateDoneMsg:
		if msg.err == nil {
			m.tabbedWindow.UpdatePipelineData(msg.items)
		}
		return m, pipelineTick()
	case tea.MouseMsg:
		// Handle mouse wheel events for scrolling the diff/preview pane
		if msg.Action == tea.MouseActionPress {
			if msg.Button == tea.MouseButtonWheelDown || msg.Button == tea.MouseButtonWheelUp {
				selected := m.list.GetSelectedInstance()
				if selected == nil || selected.Status == session.Paused {
					return m, nil
				}

				switch msg.Button {
				case tea.MouseButtonWheelUp:
					m.tabbedWindow.ScrollUp()
				case tea.MouseButtonWheelDown:
					m.tabbedWindow.ScrollDown()
				}
			}
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		m.updateHandleWindowSizeEvent(msg)
		return m, nil
	case error:
		// Handle errors from confirmation actions
		return m, m.handleError(msg)
	case instanceChangedMsg:
		// Handle instance changed after confirmation action
		return m, m.instanceChanged()
	case instanceStartedMsg:
		// Select the instance that just started (or failed)
		m.list.SelectInstance(msg.instance)

		if msg.err != nil {
			m.list.Kill()
			return m, tea.Batch(m.handleError(msg.err), m.instanceChanged())
		}

		// Save after successful start
		if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
			return m, m.handleError(err)
		}
		if m.autoYes {
			msg.instance.AutoYes = true
		}

		if msg.promptAfterName {
			m.state = statePrompt
			m.menu.SetState(ui.StatePrompt)
			m.textInputOverlay = overlay.NewTextInputOverlay("Enter prompt", "")
		} else {
			m.menu.SetState(ui.StateDefault)
			m.showHelpScreen(helpStart(msg.instance), nil)
		}

		return m, tea.Batch(tea.WindowSize(), m.instanceChanged())
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *home) handleQuit() (tea.Model, tea.Cmd) {
	if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
		return m, m.handleError(err)
	}
	return m, tea.Quit
}

func (m *home) handleMenuHighlighting(msg tea.KeyMsg) (cmd tea.Cmd, returnEarly bool) {
	// Handle menu highlighting when you press a button. We intercept it here and immediately return to
	// update the ui while re-sending the keypress. Then, on the next call to this, we actually handle the keypress.
	if m.keySent {
		m.keySent = false
		return nil, false
	}
	if m.state == statePrompt || m.state == stateHelp || m.state == stateConfirm ||
		m.state == stateNewMenu || m.state == stateRepoSelect || m.state == stateAttachList ||
		m.state == stateAttachPath || m.state == stateDirSelect || m.state == stateDirPath {
		return nil, false
	}
	// If it's in the global keymap, we should try to highlight it.
	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return nil, false
	}

	if m.list.GetSelectedInstance() != nil && m.list.GetSelectedInstance().Paused() && name == keys.KeyEnter {
		return nil, false
	}
	if name == keys.KeyShiftDown || name == keys.KeyShiftUp {
		return nil, false
	}

	// Skip the menu highlighting if the key is not in the map or we are using the shift up and down keys.
	// TODO: cleanup: when you press enter on stateNew, we use keys.KeySubmitName. We should unify the keymap.
	if name == keys.KeyEnter && m.state == stateNew {
		name = keys.KeySubmitName
	}
	m.keySent = true
	return tea.Batch(
		func() tea.Msg { return msg },
		m.keydownCallback(name)), true
}

func (m *home) handleKeyPress(msg tea.KeyMsg) (mod tea.Model, cmd tea.Cmd) {
	cmd, returnEarly := m.handleMenuHighlighting(msg)
	if returnEarly {
		return m, cmd
	}

	if m.state == stateHelp {
		return m.handleHelpState(msg)
	}

	if m.state == stateNew {
		// Handle quit commands first. Don't handle q because the user might want to type that.
		if msg.String() == "ctrl+c" {
			m.state = stateDefault
			m.promptAfterName = false
			m.list.Kill()
			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		}

		instance := m.list.GetInstances()[m.list.NumInstances()-1]
		switch msg.Type {
		// Start the instance (enable previews etc) and go back to the main menu state.
		case tea.KeyEnter:
			if len(instance.Title) == 0 {
				return m, m.handleError(fmt.Errorf("title cannot be empty"))
			}

			// Set Loading status and finalize into the list immediately
			instance.SetStatus(session.Loading)
			m.newInstanceFinalizer()
			promptAfterName := m.promptAfterName
			m.promptAfterName = false
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)

			// Return a tea.Cmd that runs instance.Start in the background
			startCmd := func() tea.Msg {
				err := instance.Start(true)
				return instanceStartedMsg{
					instance:        instance,
					err:             err,
					promptAfterName: promptAfterName,
				}
			}

			return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), startCmd)
		case tea.KeyRunes:
			if runewidth.StringWidth(instance.Title) >= 32 {
				return m, m.handleError(fmt.Errorf("title cannot be longer than 32 characters"))
			}
			if err := instance.SetTitle(instance.Title + string(msg.Runes)); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyBackspace:
			runes := []rune(instance.Title)
			if len(runes) == 0 {
				return m, nil
			}
			if err := instance.SetTitle(string(runes[:len(runes)-1])); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeySpace:
			if err := instance.SetTitle(instance.Title + " "); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyEsc:
			m.list.Kill()
			m.state = stateDefault
			m.instanceChanged()

			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		default:
		}
		return m, nil
	} else if m.state == statePrompt {
		// Use the new TextInputOverlay component to handle all key events
		shouldClose := m.textInputOverlay.HandleKeyPress(msg)

		// Check if the form was submitted or canceled
		if shouldClose {
			selected := m.list.GetSelectedInstance()
			// TODO: this should never happen since we set the instance in the previous state.
			if selected == nil {
				return m, nil
			}
			if m.textInputOverlay.IsSubmitted() {
				if err := selected.SendPrompt(m.textInputOverlay.GetValue()); err != nil {
					// TODO: we probably end up in a bad state here.
					return m, m.handleError(err)
				}
			}

			// Close the overlay and reset state
			m.textInputOverlay = nil
			m.state = stateDefault
			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					m.showHelpScreen(helpStart(selected), nil)
					return nil
				},
			)
		}

		return m, nil
	}

	// Handle new-instance submenu (n key → w/n/d options)
	if m.state == stateNewMenu {
		switch msg.String() {
		case "w":
			// New worktree — show repo picker
			repos := m.discoverRepos()
			if len(repos) == 0 {
				m.state = stateDefault
				return m, m.handleError(fmt.Errorf("no git repositories found — use 'n' for no worktree"))
			}
			if len(repos) == 1 {
				// Only one repo, skip the picker and use it directly.
				m.selectedRepoPath = repos[0].Value
				if m.list.NumInstances() >= GlobalInstanceLimit {
					m.state = stateDefault
					return m, m.handleError(fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
				}
				instance, err := session.NewInstance(session.InstanceOptions{
					Title:   "",
					Path:    m.selectedRepoPath,
					Program: m.program,
				})
				if err != nil {
					m.state = stateDefault
					return m, m.handleError(err)
				}
				m.newInstanceFinalizer = m.list.AddInstance(instance)
				m.list.SetSelectedInstance(m.list.NumInstances() - 1)
				m.state = stateNew
				m.menu.SetState(ui.StateNewInstance)
				return m, nil
			}
			// Multiple repos — show picker
			m.selectionOverlay = overlay.NewSelectionOverlay(
				"Select Repository",
				repos,
				"↑/↓ navigate • enter select • esc cancel",
			)
			m.selectionOverlay.SetWidth(60)
			m.state = stateRepoSelect
			return m, nil
		case "n":
			// No worktree, run in current directory
			if m.list.NumInstances() >= GlobalInstanceLimit {
				m.state = stateDefault
				return m, m.handleError(fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
			}
			instance, err := session.NewInstance(session.InstanceOptions{
				Title:        "",
				Path:         ".",
				Program:      m.program,
				WorktreeMode: session.WorktreeNone,
			})
			if err != nil {
				m.state = stateDefault
				return m, m.handleError(err)
			}
			m.newInstanceFinalizer = m.list.AddInstance(instance)
			m.list.SetSelectedInstance(m.list.NumInstances() - 1)
			m.state = stateNew
			m.menu.SetState(ui.StateNewInstance)
			return m, nil
		case "d":
			// No worktree, different directory — show directory picker
			dirs := m.discoverDirectories()
			if len(dirs) == 0 {
				// No known directories, fall back to manual path entry
				m.dirPath = ""
				m.state = stateDirPath
				return m, nil
			}
			m.selectionOverlay = overlay.NewSelectionOverlay(
				"Select Directory",
				dirs,
				"↑/↓ navigate • enter select • p type path • esc cancel",
			)
			m.selectionOverlay.SetWidth(60)
			m.state = stateDirSelect
			return m, nil
		case "esc", "ctrl+c":
			m.state = stateDefault
			return m, nil
		}
		return m, nil
	}

	// Handle repo selection for new worktree
	if m.state == stateRepoSelect {
		shouldClose := m.selectionOverlay.HandleKeyPress(msg)
		if shouldClose {
			if m.selectionOverlay.Selected {
				item := m.selectionOverlay.GetSelectedItem()
				if item == nil {
					m.state = stateDefault
					m.selectionOverlay = nil
					return m, nil
				}
				m.selectedRepoPath = item.Value
				m.selectionOverlay = nil

				if m.list.NumInstances() >= GlobalInstanceLimit {
					m.state = stateDefault
					return m, m.handleError(fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
				}
				instance, err := session.NewInstance(session.InstanceOptions{
					Title:   "",
					Path:    m.selectedRepoPath,
					Program: m.program,
				})
				if err != nil {
					m.state = stateDefault
					return m, m.handleError(err)
				}
				m.newInstanceFinalizer = m.list.AddInstance(instance)
				m.list.SetSelectedInstance(m.list.NumInstances() - 1)
				m.state = stateNew
				m.menu.SetState(ui.StateNewInstance)
				return m, nil
			}
			// Cancelled
			m.state = stateDefault
			m.selectionOverlay = nil
			return m, nil
		}
		return m, nil
	}

	// Handle worktree list selection state
	if m.state == stateAttachList {
		// "p" falls back to manual path entry
		if msg.String() == "p" {
			m.attachMode = session.WorktreeExisting
			m.attachPath = ""
			m.selectionOverlay = nil
			m.state = stateAttachPath
			return m, nil
		}

		shouldClose := m.selectionOverlay.HandleKeyPress(msg)
		if shouldClose {
			if m.selectionOverlay.Selected {
				item := m.selectionOverlay.GetSelectedItem()
				if item == nil {
					m.state = stateDefault
					m.selectionOverlay = nil
					return m, nil
				}
				worktreePath := item.Value

				// Duplicate detection: check if any running instance already uses this path
				for _, inst := range m.list.GetInstances() {
					if inst.Path == worktreePath {
						m.state = stateDefault
						m.selectionOverlay = nil
						return m, m.handleError(fmt.Errorf("worktree already managed by instance '%s'", inst.Title))
					}
				}

				if m.list.NumInstances() >= GlobalInstanceLimit {
					m.state = stateDefault
					m.selectionOverlay = nil
					return m, m.handleError(fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
				}

				instance, err := session.NewInstance(session.InstanceOptions{
					Title:                "",
					Path:                 ".",
					Program:              m.program,
					WorktreeMode:         session.WorktreeExisting,
					ExistingWorktreePath: worktreePath,
				})
				if err != nil {
					m.state = stateDefault
					m.selectionOverlay = nil
					return m, m.handleError(err)
				}

				// Pre-fill title from branch name, stripping the branch prefix
				branchName := item.Label
				cfg := config.LoadConfig()
				if strings.HasPrefix(branchName, cfg.BranchPrefix) {
					branchName = strings.TrimPrefix(branchName, cfg.BranchPrefix)
				}
				if err := instance.SetTitle(branchName); err != nil {
					log.ErrorLog.Printf("failed to pre-fill title: %v", err)
				}

				m.newInstanceFinalizer = m.list.AddInstance(instance)
				m.list.SetSelectedInstance(m.list.NumInstances() - 1)
				m.selectionOverlay = nil
				m.state = stateNew
				m.menu.SetState(ui.StateNewInstance)
				return m, nil
			}
			// Cancelled
			m.state = stateDefault
			m.selectionOverlay = nil
			return m, nil
		}
		return m, nil
	}

	// Handle attach path entry state
	if m.state == stateAttachPath {
		switch msg.Type {
		case tea.KeyEnter:
			if m.attachPath == "" {
				return m, m.handleError(fmt.Errorf("path cannot be empty"))
			}
			// Validate the path is a git worktree or at least a git repo
			if !git.IsGitRepo(m.attachPath) {
				return m, m.handleError(fmt.Errorf("path is not a git repository: %s", m.attachPath))
			}
			if m.list.NumInstances() >= GlobalInstanceLimit {
				m.state = stateDefault
				return m, m.handleError(fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
			}
			instance, err := session.NewInstance(session.InstanceOptions{
				Title:                "",
				Path:                 ".",
				Program:              m.program,
				WorktreeMode:         session.WorktreeExisting,
				ExistingWorktreePath: m.attachPath,
			})
			if err != nil {
				m.state = stateDefault
				return m, m.handleError(err)
			}
			m.newInstanceFinalizer = m.list.AddInstance(instance)
			m.list.SetSelectedInstance(m.list.NumInstances() - 1)
			m.state = stateNew
			m.menu.SetState(ui.StateNewInstance)
			return m, nil
		case tea.KeyRunes:
			m.attachPath += string(msg.Runes)
		case tea.KeyBackspace:
			runes := []rune(m.attachPath)
			if len(runes) > 0 {
				m.attachPath = string(runes[:len(runes)-1])
			}
		case tea.KeySpace:
			m.attachPath += " "
		case tea.KeyEsc:
			m.state = stateDefault
			return m, nil
		}
		return m, nil
	}

	// Handle directory selection state (no worktree, different directory)
	if m.state == stateDirSelect {
		// "p" falls back to manual path entry
		if msg.String() == "p" {
			m.dirPath = ""
			m.selectionOverlay = nil
			m.state = stateDirPath
			return m, nil
		}

		shouldClose := m.selectionOverlay.HandleKeyPress(msg)
		if shouldClose {
			if m.selectionOverlay.Selected {
				item := m.selectionOverlay.GetSelectedItem()
				if item == nil {
					m.state = stateDefault
					m.selectionOverlay = nil
					return m, nil
				}
				selectedDir := item.Value
				m.selectionOverlay = nil

				if m.list.NumInstances() >= GlobalInstanceLimit {
					m.state = stateDefault
					return m, m.handleError(fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
				}
				instance, err := session.NewInstance(session.InstanceOptions{
					Title:        "",
					Path:         selectedDir,
					Program:      m.program,
					WorktreeMode: session.WorktreeNone,
				})
				if err != nil {
					m.state = stateDefault
					return m, m.handleError(err)
				}
				m.newInstanceFinalizer = m.list.AddInstance(instance)
				m.list.SetSelectedInstance(m.list.NumInstances() - 1)
				m.state = stateNew
				m.menu.SetState(ui.StateNewInstance)
				return m, nil
			}
			// Cancelled
			m.state = stateDefault
			m.selectionOverlay = nil
			return m, nil
		}
		return m, nil
	}

	// Handle directory path entry state (manual fallback)
	if m.state == stateDirPath {
		switch msg.Type {
		case tea.KeyEnter:
			if m.dirPath == "" {
				return m, m.handleError(fmt.Errorf("path cannot be empty"))
			}
			// Validate the path exists and is a directory
			info, err := os.Stat(m.dirPath)
			if err != nil {
				return m, m.handleError(fmt.Errorf("path does not exist: %s", m.dirPath))
			}
			if !info.IsDir() {
				return m, m.handleError(fmt.Errorf("path is not a directory: %s", m.dirPath))
			}
			if m.list.NumInstances() >= GlobalInstanceLimit {
				m.state = stateDefault
				return m, m.handleError(fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
			}
			instance, err := session.NewInstance(session.InstanceOptions{
				Title:        "",
				Path:         m.dirPath,
				Program:      m.program,
				WorktreeMode: session.WorktreeNone,
			})
			if err != nil {
				m.state = stateDefault
				return m, m.handleError(err)
			}
			m.newInstanceFinalizer = m.list.AddInstance(instance)
			m.list.SetSelectedInstance(m.list.NumInstances() - 1)
			m.state = stateNew
			m.menu.SetState(ui.StateNewInstance)
			return m, nil
		case tea.KeyRunes:
			m.dirPath += string(msg.Runes)
		case tea.KeyBackspace:
			runes := []rune(m.dirPath)
			if len(runes) > 0 {
				m.dirPath = string(runes[:len(runes)-1])
			}
		case tea.KeySpace:
			m.dirPath += " "
		case tea.KeyEsc:
			m.state = stateDefault
			return m, nil
		}
		return m, nil
	}

	// Handle confirmation state
	if m.state == stateConfirm {
		shouldClose := m.confirmationOverlay.HandleKeyPress(msg)
		if shouldClose {
			m.state = stateDefault
			m.confirmationOverlay = nil
			return m, nil
		}
		return m, nil
	}

	// Exit scrolling mode when ESC is pressed and preview pane is in scrolling mode
	// Check if Escape key was pressed and we're not in the diff tab (meaning we're in preview tab)
	// Always check for escape key first to ensure it doesn't get intercepted elsewhere
	if msg.Type == tea.KeyEsc {
		// If in preview tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInPreviewTab() && m.tabbedWindow.IsPreviewInScrollMode() {
			// Use the selected instance from the list
			selected := m.list.GetSelectedInstance()
			err := m.tabbedWindow.ResetPreviewToNormalMode(selected)
			if err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		// If in terminal tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInTerminalTab() && m.tabbedWindow.IsTerminalInScrollMode() {
			m.tabbedWindow.ResetTerminalToNormalMode()
			return m, m.instanceChanged()
		}
	}

	// Handle quit commands first
	if msg.String() == "ctrl+c" || msg.String() == "q" {
		return m.handleQuit()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}

	switch name {
	case keys.KeyAttach:
		// Attach to existing worktree — go directly to worktree list picker
		cwd, _ := os.Getwd()
		if !git.IsGitRepo(cwd) {
			return m, m.handleError(fmt.Errorf("not in a git repository — no worktrees to attach to"))
		}
		worktrees, err := git.ListWorktrees(cwd)
		if err != nil {
			return m, m.handleError(fmt.Errorf("failed to list worktrees: %w", err))
		}
		if len(worktrees) == 0 {
			return m, m.handleError(fmt.Errorf("no linked worktrees found — use 'n' then 'w' to create a new one"))
		}
		items := make([]overlay.SelectionItem, len(worktrees))
		for i, wt := range worktrees {
			items[i] = overlay.SelectionItem{
				Label:       wt.Branch,
				Description: wt.Path,
				Value:       wt.Path,
			}
		}
		m.selectionOverlay = overlay.NewSelectionOverlay(
			"Attach to Existing Worktree",
			items,
			"↑/↓ navigate • enter select • p type path • esc cancel",
		)
		m.selectionOverlay.SetWidth(55)
		m.state = stateAttachList
		return m, nil
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral{}, nil)
	case keys.KeyPrompt:
		if m.list.NumInstances() >= GlobalInstanceLimit {
			return m, m.handleError(
				fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
		}
		instance, err := session.NewInstance(session.InstanceOptions{
			Title:   "",
			Path:    ".",
			Program: m.program,
		})
		if err != nil {
			return m, m.handleError(err)
		}

		m.newInstanceFinalizer = m.list.AddInstance(instance)
		m.list.SetSelectedInstance(m.list.NumInstances() - 1)
		m.state = stateNew
		m.menu.SetState(ui.StateNewInstance)
		m.promptAfterName = true

		return m, nil
	case keys.KeyNew:
		if m.list.NumInstances() >= GlobalInstanceLimit {
			return m, m.handleError(
				fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
		}
		m.state = stateNewMenu
		return m, nil
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyShiftUp:
		m.tabbedWindow.ScrollUp()
		return m, m.instanceChanged()
	case keys.KeyShiftDown:
		m.tabbedWindow.ScrollDown()
		return m, m.instanceChanged()
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyKill:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}

		// Create the kill action as a tea.Cmd
		killAction := func() tea.Msg {
			// Only check branch checkout for worktree-backed instances
			if selected.WorktreeMode == session.WorktreeNew {
				worktree, err := selected.GetGitWorktree()
				if err != nil {
					return err
				}

				checkedOut, err := worktree.IsBranchCheckedOut()
				if err != nil {
					return err
				}

				if checkedOut {
					return fmt.Errorf("instance %s is currently checked out", selected.Title)
				}
			}

			// Clean up terminal session for this instance
			m.tabbedWindow.CleanupTerminalForInstance(selected.Title)

			// Delete from storage first
			if err := m.storage.DeleteInstance(selected.Title); err != nil {
				return err
			}

			// Then kill the instance
			m.list.Kill()
			return instanceChangedMsg{}
		}

		// Show confirmation modal
		message := fmt.Sprintf("[!] Kill session '%s'?", selected.Title)
		return m, m.confirmAction(message, killAction)
	case keys.KeySubmit:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}
		if selected.WorktreeMode == session.WorktreeNone {
			return m, m.handleError(fmt.Errorf("no worktree to push from"))
		}

		// Create the push action as a tea.Cmd
		pushAction := func() tea.Msg {
			// Default commit message with timestamp
			commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s", selected.Title, time.Now().Format(time.RFC822))
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			if err = worktree.PushChanges(commitMsg, true); err != nil {
				return err
			}
			return nil
		}

		// Show confirmation modal
		message := fmt.Sprintf("[!] Push changes from session '%s'?", selected.Title)
		return m, m.confirmAction(message, pushAction)
	case keys.KeyCheckout:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}
		if selected.WorktreeMode == session.WorktreeNone {
			return m, m.handleError(fmt.Errorf("no worktree to checkout"))
		}

		// Show help screen before pausing
		m.showHelpScreen(helpTypeInstanceCheckout{}, func() {
			if err := selected.Pause(); err != nil {
				m.handleError(err)
			}
			m.tabbedWindow.CleanupTerminalForInstance(selected.Title)
			m.instanceChanged()
		})
		return m, nil
	case keys.KeyResume:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}
		if err := selected.Resume(); err != nil {
			return m, m.handleError(err)
		}
		return m, tea.WindowSize()
	case keys.KeyEnter:
		// Pipeline tab: jump to matched instance
		if m.tabbedWindow.IsInPipelineTab() {
			item := m.tabbedWindow.GetPipelineJumpTarget()
			if item != nil && item.InstanceIdx >= 0 {
				m.list.SetSelectedInstance(item.InstanceIdx)
				m.tabbedWindow.SetActiveTab(ui.PreviewTab)
				m.menu.SetActiveTab(ui.PreviewTab)
				return m, m.instanceChanged()
			}
			return m, nil
		}
		if m.list.NumInstances() == 0 {
			return m, nil
		}
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Paused() || selected.Status == session.Loading || !selected.TmuxAlive() {
			return m, nil
		}
		// Terminal tab: attach to terminal session
		if m.tabbedWindow.IsInTerminalTab() {
			m.showHelpScreen(helpTypeInstanceAttach{}, func() {
				ch, err := m.tabbedWindow.AttachTerminal()
				if err != nil {
					m.handleError(err)
					return
				}
				<-ch
				m.state = stateDefault
			})
			return m, nil
		}
		// Show help screen before attaching
		m.showHelpScreen(helpTypeInstanceAttach{}, func() {
			ch, err := m.list.Attach()
			if err != nil {
				m.handleError(err)
				return
			}
			<-ch
			m.state = stateDefault
			m.instanceChanged()
		})
		return m, nil
	default:
		return m, nil
	}
}

// instanceChanged updates the preview pane, menu, and diff pane based on the selected instance. It returns an error
// Cmd if there was any error.
func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	m.tabbedWindow.SetInstance(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	// If there's no selected instance, we don't need to update the preview.
	if err := m.tabbedWindow.UpdatePreview(selected); err != nil {
		return m.handleError(err)
	}
	if err := m.tabbedWindow.UpdateTerminal(selected); err != nil {
		return m.handleError(err)
	}
	return nil
}

type keyupMsg struct{}

// keydownCallback clears the menu option highlighting after 500ms.
func (m *home) keydownCallback(name keys.KeyName) tea.Cmd {
	m.menu.Keydown(name)
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}

		return keyupMsg{}
	}
}

// hideErrMsg implements tea.Msg and clears the error text from the screen.
type hideErrMsg struct{}

// previewTickMsg implements tea.Msg and triggers a preview update
type previewTickMsg struct{}

type instanceChangedMsg struct{}

// instanceMetaResult holds the results of a single instance's metadata update,
// computed in a background goroutine.
type instanceMetaResult struct {
	instance  *session.Instance
	updated   bool
	hasPrompt bool
	diffStats *git.DiffStats
}

// metadataUpdateDoneMsg is sent when the background metadata update completes.
type metadataUpdateDoneMsg struct {
	results []instanceMetaResult
}

type instanceStartedMsg struct {
	instance        *session.Instance
	err             error
	promptAfterName bool
}

// pipelineTickMsg triggers a pipeline data refresh.
type pipelineTickMsg struct{}

// pipelineUpdateDoneMsg carries refreshed pipeline data back to the main loop.
type pipelineUpdateDoneMsg struct {
	items []pipeline.Item
	err   error
}

// tickUpdateMetadataCmd returns a self-chaining Cmd that sleeps 500ms, then performs
// expensive metadata I/O (tmux capture, git diff) in parallel background goroutines.
// Because it only re-schedules after completing, overlapping ticks are impossible.
func tickUpdateMetadataCmd(instances []*session.Instance) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(500 * time.Millisecond)

		var active []*session.Instance
		for _, inst := range instances {
			if inst.Started() && !inst.Paused() {
				active = append(active, inst)
			}
		}
		if len(active) == 0 {
			return metadataUpdateDoneMsg{}
		}

		results := make([]instanceMetaResult, len(active))
		var wg sync.WaitGroup
		for idx, inst := range active {
			wg.Add(1)
			go func(i int, instance *session.Instance) {
				defer wg.Done()
				r := &results[i]
				r.instance = instance
				r.instance.CheckAndHandleTrustPrompt()
				r.updated, r.hasPrompt = instance.HasUpdated()
				r.diffStats = instance.ComputeDiff()
			}(idx, inst)
		}
		wg.Wait()

		return metadataUpdateDoneMsg{results: results}
	}
}

// pipelineTick returns a Cmd that sleeps 5 seconds then triggers a pipeline refresh.
func pipelineTick() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return pipelineTickMsg{}
	}
}

// handleError handles all errors which get bubbled up to the app. sets the error message. We return a callback tea.Cmd that returns a hideErrMsg message
// which clears the error message after 3 seconds.
func (m *home) handleError(err error) tea.Cmd {
	log.ErrorLog.Printf("%v", err)
	m.errBox.SetError(err)
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(3 * time.Second):
		}

		return hideErrMsg{}
	}
}

// confirmAction shows a confirmation modal and stores the action to execute on confirm
func (m *home) confirmAction(message string, action tea.Cmd) tea.Cmd {
	m.state = stateConfirm

	// Create and show the confirmation overlay using ConfirmationOverlay
	m.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	// Set a fixed width for consistent appearance
	m.confirmationOverlay.SetWidth(50)

	// Set callbacks for confirmation and cancellation
	m.confirmationOverlay.OnConfirm = func() {
		m.state = stateDefault
		// Execute the action if it exists
		if action != nil {
			_ = action()
		}
	}

	m.confirmationOverlay.OnCancel = func() {
		m.state = stateDefault
	}

	return nil
}

func (m *home) View() string {
	listWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.list.String())
	previewWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.tabbedWindow.String())
	listAndPreview := lipgloss.JoinHorizontal(lipgloss.Top, listWithPadding, previewWithPadding)

	mainView := lipgloss.JoinVertical(
		lipgloss.Center,
		listAndPreview,
		m.menu.String(),
		m.errBox.String(),
	)

	if m.state == statePrompt {
		if m.textInputOverlay == nil {
			log.ErrorLog.Printf("text input overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textInputOverlay.Render(), mainView, true, true)
	} else if m.state == stateHelp {
		if m.textOverlay == nil {
			log.ErrorLog.Printf("text overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textOverlay.Render(), mainView, true, true)
	} else if m.state == stateConfirm {
		if m.confirmationOverlay == nil {
			log.ErrorLog.Printf("confirmation overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.confirmationOverlay.Render(), mainView, true, true)
	} else if m.state == stateNewMenu {
		newMenuOverlay := m.renderNewMenuOverlay()
		return overlay.PlaceOverlay(0, 0, newMenuOverlay, mainView, true, true)
	} else if m.state == stateRepoSelect {
		if m.selectionOverlay == nil {
			log.ErrorLog.Printf("selection overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.selectionOverlay.Render(), mainView, true, true)
	} else if m.state == stateAttachList {
		if m.selectionOverlay == nil {
			log.ErrorLog.Printf("selection overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.selectionOverlay.Render(), mainView, true, true)
	} else if m.state == stateAttachPath {
		pathOverlay := m.renderAttachPathOverlay()
		return overlay.PlaceOverlay(0, 0, pathOverlay, mainView, true, true)
	} else if m.state == stateDirSelect {
		if m.selectionOverlay == nil {
			log.ErrorLog.Printf("selection overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.selectionOverlay.Render(), mainView, true, true)
	} else if m.state == stateDirPath {
		dirOverlay := m.renderDirPathOverlay()
		return overlay.PlaceOverlay(0, 0, dirOverlay, mainView, true, true)
	}

	return mainView
}

// renderNewMenuOverlay renders the new-instance mode selector overlay (n key submenu).
func (m *home) renderNewMenuOverlay() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2).
		Width(55)

	highlightKey := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFCC00"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#777777"))

	// Show the configured branch prefix so users know the naming convention.
	cfg := config.LoadConfig()
	branchHint := fmt.Sprintf("(branch: %s{name})", cfg.BranchPrefix)

	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Color("#7D56F4")).Render("New Instance"),
		"",
		highlightKey.Render("w")+"   New worktree "+dimStyle.Render(branchHint),
		highlightKey.Render("n")+"   No worktree (current directory)",
		highlightKey.Render("d")+"   No worktree (different directory)",
		"",
		dimStyle.Render("esc to cancel"),
	)
	return style.Render(content)
}

// discoverRepos finds known git repositories from existing CS instances and the cwd.
func (m *home) discoverRepos() []overlay.SelectionItem {
	seen := make(map[string]bool)
	var items []overlay.SelectionItem

	// 1. Current working directory (if it's a git repo)
	cwd, _ := os.Getwd()
	if git.IsGitRepo(cwd) {
		if root, err := git.FindGitRepoRoot(cwd); err == nil && !seen[root] {
			seen[root] = true
			items = append(items, overlay.SelectionItem{
				Label:       filepath.Base(root),
				Description: root,
				Value:       root,
			})
		}
	}

	// 2. Repos from existing CS instances (via their stored worktree repo paths)
	for _, inst := range m.list.GetInstances() {
		wt := inst.GetWorktreePath()
		if wt == "" {
			continue
		}
		// Try to resolve the repo root from the stored path
		if root, err := git.FindGitRepoRoot(wt); err == nil && !seen[root] {
			seen[root] = true
			items = append(items, overlay.SelectionItem{
				Label:       filepath.Base(root),
				Description: root,
				Value:       root,
			})
		}
	}

	// 3. Also check instance.Path for WorktreeNone instances that might point to a repo
	for _, inst := range m.list.GetInstances() {
		if inst.Path != "" && git.IsGitRepo(inst.Path) {
			if root, err := git.FindGitRepoRoot(inst.Path); err == nil && !seen[root] {
				seen[root] = true
				items = append(items, overlay.SelectionItem{
					Label:       filepath.Base(root),
					Description: root,
					Value:       root,
				})
			}
		}
	}

	// 4. Scan well-known project directories for git repos
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, dir := range []string{"projects", "claude"} {
			parentDir := filepath.Join(home, dir)
			if entries, err := os.ReadDir(parentDir); err == nil {
				for _, entry := range entries {
					if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
						subdir := filepath.Join(parentDir, entry.Name())
						if git.IsGitRepo(subdir) {
							if root, err := git.FindGitRepoRoot(subdir); err == nil && !seen[root] {
								seen[root] = true
								items = append(items, overlay.SelectionItem{
									Label:       filepath.Base(root),
									Description: root,
									Value:       root,
								})
							}
						}
					}
				}
			}
		}
	}

	return items
}

// discoverDirectories finds known directories for the directory picker.
// Sources: instance paths, worktree paths, home directory, and ~/projects/ subdirectories.
func (m *home) discoverDirectories() []overlay.SelectionItem {
	seen := make(map[string]bool)
	var items []overlay.SelectionItem

	addDir := func(dir, label string) {
		if dir == "" || seen[dir] {
			return
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		seen[dir] = true
		if label == "" {
			label = filepath.Base(dir)
		}
		items = append(items, overlay.SelectionItem{
			Label:       label,
			Description: dir,
			Value:       dir,
		})
	}

	// 1. Current working directory
	cwd, _ := os.Getwd()
	addDir(cwd, filepath.Base(cwd)+" (cwd)")

	// 2. Paths from existing CS instances
	for _, inst := range m.list.GetInstances() {
		addDir(inst.Path, "")
		addDir(inst.GetWorktreePath(), "")
	}

	// 3. Home directory
	home, _ := os.UserHomeDir()
	if home != "" {
		addDir(home, "~ (home)")
	}

	// 4. Scan well-known project directories for subdirectories
	if home != "" {
		for _, dir := range []string{"projects", "claude"} {
			parentDir := filepath.Join(home, dir)
			if entries, err := os.ReadDir(parentDir); err == nil {
				for _, entry := range entries {
					if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
						addDir(filepath.Join(parentDir, entry.Name()), "")
					}
				}
			}
		}
	}

	return items
}

// renderDirPathOverlay renders the directory path entry overlay for no-worktree different-dir mode.
func (m *home) renderDirPathOverlay() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2).
		Width(60)

	pathDisplay := m.dirPath
	if pathDisplay == "" {
		pathDisplay = lipgloss.NewStyle().Foreground(lipgloss.Color("#777777")).Render("(type path to directory)")
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("Enter directory path:"),
		"",
		pathDisplay+"█",
		"",
		lipgloss.NewStyle().Foreground(lipgloss.Color("#777777")).Render("enter to confirm, esc to cancel"),
	)
	return style.Render(content)
}

// renderAttachPathOverlay renders the path entry overlay for existing worktree attach.
func (m *home) renderAttachPathOverlay() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2).
		Width(60)

	pathDisplay := m.attachPath
	if pathDisplay == "" {
		pathDisplay = lipgloss.NewStyle().Foreground(lipgloss.Color("#777777")).Render("(type path to worktree)")
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("Enter worktree path:"),
		"",
		pathDisplay+"█",
		"",
		lipgloss.NewStyle().Foreground(lipgloss.Color("#777777")).Render("enter to confirm, esc to cancel"),
	)
	return style.Render(content)
}
