// Package tui implements the ccdc-cli dashboard. Everything the flag-based
// CLI can do (manage targets, run inventory, back up, restore, roll k8
// credentials) is reachable from here too, so a fresh install can be run
// entirely through `ccdc-cli tui` without ever touching the flag commands.
// It's a thin layer over the existing module packages - it calls their
// exported *Capture functions rather than reimplementing any inventory,
// backup, restore, or rotation logic, so the CLI and TUI can't drift apart
// on what they actually do.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ccdc-cli/config"
	"ccdc-cli/k8Module"
	"ccdc-cli/mysqlModule"
	"ccdc-cli/psqlModule"
	"ccdc-cli/utils"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------
// Screens and actions
// ---------------------------------------------------------------------

type screen int

const (
	screenTargetList screen = iota
	screenAddTargetType
	screenAddTargetFields
	screenConfirmDelete
	screenActionMenu
	screenFilePrompt
	screenPasswordPrompt
	screenSequenceInput
	screenConfirmAction
	screenRunning
	screenOutput
	screenError

	// k8 interactive secret browser
	screenK8NamespaceList
	screenK8SecretList
	screenK8KeyList
	screenK8KeyDetail
	screenK8Strategy
	screenK8UserValueInput
)

type actionType int

const (
	actionInventory actionType = iota
	actionBackup
	actionRestore
	actionRollCredentials
	actionBrowseRotate     // shown in the k8 action menu
	actionRotateSecretValue // internal only - result of the browse flow, never shown in a menu
)

func (a actionType) label() string {
	switch a {
	case actionInventory:
		return "Inventory"
	case actionBackup:
		return "Backup"
	case actionRestore:
		return "Restore"
	case actionRollCredentials:
		return "Rotate (raw sequence)"
	case actionBrowseRotate:
		return "Browse & Rotate Secret"
	case actionRotateSecretValue:
		return "Rotate Secret"
	default:
		return "?"
	}
}

func actionsFor(t config.TargetType) []actionType {
	switch t {
	case config.TypeMySQL, config.TypePsql:
		return []actionType{actionInventory, actionBackup, actionRestore}
	case config.TypeK8s:
		return []actionType{actionInventory, actionBrowseRotate, actionRollCredentials}
	default:
		return nil
	}
}

var addableTypes = []config.TargetType{config.TypeMySQL, config.TypePsql, config.TypeK8s}

// ---------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	typeTagStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("242"))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117"))

	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true)
)

// ---------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------

type inventoryResultMsg struct {
	output string
	err    error
}

type namespacesLoadedMsg struct {
	namespaces []string
	err        error
}

type secretsLoadedMsg struct {
	secrets []k8Module.SecretSummary
	err     error
}

type keyValueLoadedMsg struct {
	value string
	err   error
}

// ---------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------

type model struct {
	screen screen

	// target list
	targets []config.Target
	cursor  int

	selected      config.Target
	actions       []actionType
	actionCursor  int
	pendingAction actionType

	// add/edit target form
	addTypeCursor int
	addType       config.TargetType
	addInputs     []textinput.Model
	addLabels     []string
	addFocus      int
	addErr        string

	// delete confirm
	deleteTarget config.Target

	// file prompt (backup/restore)
	fileInput       textinput.Model
	fileErr         string
	pendingFilePath string

	// password
	passwordInput   textinput.Model
	pendingPassword string

	// k8 roll credentials
	sequenceInput   textinput.Model
	sequenceErr     string
	pendingSequence string

	// generic destructive-action confirm
	confirmMessage string

	// k8 interactive secret browser
	k8Namespaces       []string
	k8NamespaceCursor  int
	k8Namespace        string
	k8Secrets          []k8Module.SecretSummary
	k8SecretCursor     int
	k8SecretName       string
	k8Keys             []string
	k8KeyCursor        int
	k8SelectedKey      string
	k8CurrentValue     string
	k8ValueRevealed    bool
	k8StrategyCursor   int
	k8UseRandom        bool
	k8PendingStrategy  string
	k8UserValueInput   textinput.Model
	k8UserValueErr     string

	// pending single-secret rotation (set once the browse flow reaches
	// the confirm screen; consumed by screenConfirmAction)
	pendingNamespace  string
	pendingSecretName string
	pendingKey        string
	pendingStrategy   string
	pendingUseRandom  bool
	pendingUserValue  string

	runningLabel string

	spinner  spinner.Model
	viewport viewport.Model

	lastOutput string
	reveal     bool
	errMsg     string
	statusMsg  string

	width, height int
}

// Run starts the TUI. It blocks until the user quits.
func Run() error {
	targets, err := config.LoadTargets()
	if err != nil {
		return fmt.Errorf("could not load targets: %w", err)
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))

	m := model{
		screen:        screenTargetList,
		targets:       targets,
		passwordInput: newTextInput("password"),
		fileInput:     newTextInput("/path/to/file"),
		sequenceInput: newTextInput("SECRET_NAME,NAMESPACE,KEY,STRATEGY"),
		k8UserValueInput: newTextInput("new value"),
		spinner:       sp,
		viewport:      viewport.New(80, 20),
	}
	m.passwordInput.EchoMode = textinput.EchoPassword
	m.passwordInput.EchoCharacter = '•'

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func newTextInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 512
	return ti
}

func (m model) Init() tea.Cmd {
	return nil
}

// ---------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width - 2
		m.viewport.Height = msg.Height - 6
		return m, nil

	case inventoryResultMsg:
		if msg.err != nil {
			m.screen = screenError
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.lastOutput = msg.output
		m.viewport.SetContent(msg.output)
		m.viewport.GotoTop()
		m.screen = screenOutput
		m.statusMsg = ""
		return m, nil

	case namespacesLoadedMsg:
		if msg.err != nil {
			m.screen = screenError
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.k8Namespaces = msg.namespaces
		m.k8NamespaceCursor = 0
		m.screen = screenK8NamespaceList
		return m, nil

	case secretsLoadedMsg:
		if msg.err != nil {
			m.screen = screenError
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.k8Secrets = msg.secrets
		m.k8SecretCursor = 0
		m.screen = screenK8SecretList
		return m, nil

	case keyValueLoadedMsg:
		if msg.err != nil {
			m.screen = screenError
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.k8CurrentValue = msg.value
		m.k8ValueRevealed = false
		m.screen = screenK8KeyDetail
		return m, nil

	case spinner.TickMsg:
		if m.screen == screenRunning {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.handleKey(msg)
	}

	// Any message type not handled above (e.g. the textinput cursor-blink
	// tick) gets forwarded to whichever input owns the current screen.
	var cmd tea.Cmd
	switch m.screen {
	case screenAddTargetFields:
		if m.addFocus < len(m.addInputs) {
			m.addInputs[m.addFocus], cmd = m.addInputs[m.addFocus].Update(msg)
		}
	case screenFilePrompt:
		m.fileInput, cmd = m.fileInput.Update(msg)
	case screenPasswordPrompt:
		m.passwordInput, cmd = m.passwordInput.Update(msg)
	case screenSequenceInput:
		m.sequenceInput, cmd = m.sequenceInput.Update(msg)
	case screenK8UserValueInput:
		m.k8UserValueInput, cmd = m.k8UserValueInput.Update(msg)
	case screenOutput:
		m.viewport, cmd = m.viewport.Update(msg)
	}
	return m, cmd
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {

	case screenTargetList:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.targets)-1 {
				m.cursor++
			}
		case "a":
			m.addTypeCursor = 0
			m.addErr = ""
			m.screen = screenAddTargetType
		case "e":
			if len(m.targets) > 0 {
				t := m.targets[m.cursor]
				m.addType = t.Type
				m.buildAddInputs(t)
				m.screen = screenAddTargetFields
				return m, textinput.Blink
			}
		case "d":
			if len(m.targets) > 0 {
				m.deleteTarget = m.targets[m.cursor]
				m.screen = screenConfirmDelete
			}
		case "enter":
			if len(m.targets) > 0 {
				m.selected = m.targets[m.cursor]
				m.actions = actionsFor(m.selected.Type)
				m.actionCursor = 0
				m.screen = screenActionMenu
			}
		}
		return m, nil

	case screenAddTargetType:
		switch msg.String() {
		case "esc":
			m.screen = screenTargetList
		case "up", "k":
			if m.addTypeCursor > 0 {
				m.addTypeCursor--
			}
		case "down", "j":
			if m.addTypeCursor < len(addableTypes)-1 {
				m.addTypeCursor++
			}
		case "enter":
			m.addType = addableTypes[m.addTypeCursor]
			m.buildAddInputs(config.Target{})
			m.screen = screenAddTargetFields
			return m, textinput.Blink
		}
		return m, nil

	case screenAddTargetFields:
		switch msg.String() {
		case "esc":
			m.screen = screenTargetList
			return m, nil
		case "tab", "down":
			return m, m.focusAddInput((m.addFocus + 1) % len(m.addInputs))
		case "shift+tab", "up":
			return m, m.focusAddInput((m.addFocus - 1 + len(m.addInputs)) % len(m.addInputs))
		case "enter":
			if m.addFocus < len(m.addInputs)-1 {
				return m, m.focusAddInput(m.addFocus + 1)
			}
			ok, errText := m.submitAddTarget()
			if !ok {
				m.addErr = errText
				return m, nil
			}
			targets, err := config.LoadTargets()
			if err == nil {
				m.targets = targets
				if m.cursor >= len(m.targets) {
					m.cursor = len(m.targets) - 1
				}
				if m.cursor < 0 {
					m.cursor = 0
				}
			}
			m.screen = screenTargetList
			return m, nil
		default:
			var cmd tea.Cmd
			m.addInputs[m.addFocus], cmd = m.addInputs[m.addFocus].Update(msg)
			return m, cmd
		}
		return m, nil

	case screenConfirmDelete:
		switch msg.String() {
		case "y", "enter":
			config.RemoveTarget(m.deleteTarget.Name)
			targets, err := config.LoadTargets()
			if err == nil {
				m.targets = targets
				if m.cursor >= len(m.targets) {
					m.cursor = len(m.targets) - 1
				}
				if m.cursor < 0 {
					m.cursor = 0
				}
			}
			m.screen = screenTargetList
		case "n", "esc":
			m.screen = screenTargetList
		}
		return m, nil

	case screenActionMenu:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenTargetList
		case "up", "k":
			if m.actionCursor > 0 {
				m.actionCursor--
			}
		case "down", "j":
			if m.actionCursor < len(m.actions)-1 {
				m.actionCursor++
			}
		case "enter":
			m.pendingAction = m.actions[m.actionCursor]
			switch m.pendingAction {
			case actionInventory:
				if m.selected.Type == config.TypeK8s {
					m.reveal = false
					m.runningLabel = ""
					m.screen = screenRunning
					return m, tea.Batch(m.spinner.Tick, runActionCmd(m.selected, actionInventory, "", "", "", false))
				}
				m.passwordInput.SetValue("")
				m.passwordInput.Focus()
				m.screen = screenPasswordPrompt
				return m, textinput.Blink
			case actionBackup:
				suggested := fmt.Sprintf("%s-backup-%s.sql", m.selected.Name, time.Now().Format("20060102-150405"))
				m.fileInput.SetValue(suggested)
				m.fileInput.Focus()
				m.fileErr = ""
				m.screen = screenFilePrompt
				return m, textinput.Blink
			case actionRestore:
				m.fileInput.SetValue("")
				m.fileInput.Focus()
				m.fileErr = ""
				m.screen = screenFilePrompt
				return m, textinput.Blink
			case actionRollCredentials:
				m.sequenceInput.SetValue("")
				m.sequenceInput.Focus()
				m.sequenceErr = ""
				m.screen = screenSequenceInput
				return m, textinput.Blink
			case actionBrowseRotate:
				m.runningLabel = "Loading namespaces..."
				m.screen = screenRunning
				return m, tea.Batch(m.spinner.Tick, loadNamespacesCmd(m.selected.KubeconfigPath))
			}
		}
		return m, nil

	case screenFilePrompt:
		switch msg.String() {
		case "esc":
			m.screen = screenActionMenu
			return m, nil
		case "enter":
			path := strings.TrimSpace(m.fileInput.Value())
			if path == "" {
				m.fileErr = "A file path is required"
				return m, nil
			}
			m.pendingFilePath = path
			m.passwordInput.SetValue("")
			m.passwordInput.Focus()
			m.screen = screenPasswordPrompt
			return m, textinput.Blink
		default:
			var cmd tea.Cmd
			m.fileInput, cmd = m.fileInput.Update(msg)
			return m, cmd
		}

	case screenPasswordPrompt:
		switch msg.String() {
		case "esc":
			m.passwordInput.Blur()
			if m.pendingAction == actionInventory {
				m.screen = screenActionMenu
			} else {
				m.screen = screenFilePrompt
			}
			return m, nil
		case "enter":
			password := m.passwordInput.Value()
			switch m.pendingAction {
			case actionInventory:
				m.runningLabel = ""
				m.screen = screenRunning
				return m, tea.Batch(m.spinner.Tick, runActionCmd(m.selected, actionInventory, password, "", "", false))
			case actionBackup:
				m.runningLabel = ""
				m.screen = screenRunning
				return m, tea.Batch(m.spinner.Tick, runActionCmd(m.selected, actionBackup, password, m.pendingFilePath, "", false))
			case actionRestore:
				m.pendingPassword = password
				m.confirmMessage = fmt.Sprintf(
					"This will OVERWRITE the current data on %q with the contents of:\n  %s\n\nThis cannot be undone. Continue?",
					m.selected.Name, m.pendingFilePath)
				m.screen = screenConfirmAction
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.passwordInput, cmd = m.passwordInput.Update(msg)
			return m, cmd
		}

	case screenSequenceInput:
		switch msg.String() {
		case "esc":
			m.screen = screenActionMenu
			return m, nil
		case "enter":
			seq := strings.TrimSpace(m.sequenceInput.Value())
			if seq == "" {
				m.sequenceErr = "At least one SECRET_NAME,NAMESPACE,KEY,STRATEGY sequence is required"
				return m, nil
			}
			m.pendingSequence = seq
			m.confirmMessage = fmt.Sprintf(
				"This will ROTATE the following secret(s) on %q now, overwriting their current values:\n  %s\n\nThis cannot be undone. Continue?",
				m.selected.Name, seq)
			m.screen = screenConfirmAction
			return m, nil
		default:
			var cmd tea.Cmd
			m.sequenceInput, cmd = m.sequenceInput.Update(msg)
			return m, cmd
		}

	case screenK8NamespaceList:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenActionMenu
		case "up", "k":
			if m.k8NamespaceCursor > 0 {
				m.k8NamespaceCursor--
			}
		case "down", "j":
			if m.k8NamespaceCursor < len(m.k8Namespaces)-1 {
				m.k8NamespaceCursor++
			}
		case "enter":
			if len(m.k8Namespaces) > 0 {
				m.k8Namespace = m.k8Namespaces[m.k8NamespaceCursor]
				m.runningLabel = "Loading secrets in " + m.k8Namespace + "..."
				m.screen = screenRunning
				return m, tea.Batch(m.spinner.Tick, loadSecretsCmd(m.selected.KubeconfigPath, m.k8Namespace))
			}
		}
		return m, nil

	case screenK8SecretList:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenK8NamespaceList
		case "up", "k":
			if m.k8SecretCursor > 0 {
				m.k8SecretCursor--
			}
		case "down", "j":
			if m.k8SecretCursor < len(m.k8Secrets)-1 {
				m.k8SecretCursor++
			}
		case "enter":
			if len(m.k8Secrets) > 0 {
				sec := m.k8Secrets[m.k8SecretCursor]
				if len(sec.Keys) == 0 {
					m.statusMsg = "That secret has no data keys."
					return m, nil
				}
				m.k8SecretName = sec.Name
				m.k8Keys = sec.Keys
				m.k8KeyCursor = 0
				m.screen = screenK8KeyList
			}
		}
		return m, nil

	case screenK8KeyList:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenK8SecretList
		case "up", "k":
			if m.k8KeyCursor > 0 {
				m.k8KeyCursor--
			}
		case "down", "j":
			if m.k8KeyCursor < len(m.k8Keys)-1 {
				m.k8KeyCursor++
			}
		case "enter":
			if len(m.k8Keys) > 0 {
				m.k8SelectedKey = m.k8Keys[m.k8KeyCursor]
				m.runningLabel = "Fetching current value..."
				m.screen = screenRunning
				return m, tea.Batch(m.spinner.Tick, loadKeyValueCmd(m.selected.KubeconfigPath, m.k8Namespace, m.k8SecretName, m.k8SelectedKey))
			}
		}
		return m, nil

	case screenK8KeyDetail:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenK8KeyList
		case "v":
			m.k8ValueRevealed = !m.k8ValueRevealed
		case "r":
			m.k8UseRandom = true
			m.k8StrategyCursor = 0
			m.screen = screenK8Strategy
		case "u":
			m.k8UseRandom = false
			m.k8StrategyCursor = 0
			m.screen = screenK8Strategy
		}
		return m, nil

	case screenK8Strategy:
		switch msg.String() {
		case "esc", "b":
			m.screen = screenK8KeyDetail
		case "up", "k":
			if m.k8StrategyCursor > 0 {
				m.k8StrategyCursor--
			}
		case "down", "j":
			if m.k8StrategyCursor < 1 {
				m.k8StrategyCursor++
			}
		case "enter":
			strategy := "retainPrev"
			if m.k8StrategyCursor == 1 {
				strategy = "omitPrev"
			}
			if m.k8UseRandom {
				m.pendingAction = actionRotateSecretValue
				m.pendingNamespace = m.k8Namespace
				m.pendingSecretName = m.k8SecretName
				m.pendingKey = m.k8SelectedKey
				m.pendingStrategy = strategy
				m.pendingUseRandom = true
				m.pendingUserValue = ""
				m.confirmMessage = fmt.Sprintf(
					"This will ROTATE %s.%s -> %s to a NEW RANDOM VALUE now (strategy: %s).\n\nThis cannot be undone. Continue?",
					m.k8Namespace, m.k8SecretName, m.k8SelectedKey, strategy)
				m.screen = screenConfirmAction
			} else {
				m.k8PendingStrategy = strategy
				m.k8UserValueInput.SetValue("")
				m.k8UserValueInput.Focus()
				m.k8UserValueErr = ""
				m.screen = screenK8UserValueInput
				return m, textinput.Blink
			}
		}
		return m, nil

	case screenK8UserValueInput:
		switch msg.String() {
		case "esc":
			m.k8UserValueInput.Blur()
			m.screen = screenK8Strategy
			return m, nil
		case "enter":
			value := m.k8UserValueInput.Value()
			if value == "" {
				m.k8UserValueErr = "A value is required"
				return m, nil
			}
			m.pendingAction = actionRotateSecretValue
			m.pendingNamespace = m.k8Namespace
			m.pendingSecretName = m.k8SecretName
			m.pendingKey = m.k8SelectedKey
			m.pendingStrategy = m.k8PendingStrategy
			m.pendingUseRandom = false
			m.pendingUserValue = value
			m.confirmMessage = fmt.Sprintf(
				"This will ROTATE %s.%s -> %s to a NEW USER-DEFINED VALUE now (strategy: %s).\n\nThis cannot be undone. Continue?",
				m.k8Namespace, m.k8SecretName, m.k8SelectedKey, m.k8PendingStrategy)
			m.screen = screenConfirmAction
			return m, nil
		default:
			var cmd tea.Cmd
			m.k8UserValueInput, cmd = m.k8UserValueInput.Update(msg)
			return m, cmd
		}

	case screenConfirmAction:
		switch msg.String() {
		case "y", "enter":
			m.screen = screenRunning
			switch m.pendingAction {
			case actionRestore:
				m.runningLabel = "Running restore against " + m.selected.Name + "..."
				return m, tea.Batch(m.spinner.Tick, runActionCmd(m.selected, actionRestore, m.pendingPassword, m.pendingFilePath, "", false))
			case actionRollCredentials:
				m.runningLabel = "Rotating secrets on " + m.selected.Name + "..."
				return m, tea.Batch(m.spinner.Tick, runActionCmd(m.selected, actionRollCredentials, "", "", m.pendingSequence, false))
			case actionRotateSecretValue:
				m.runningLabel = fmt.Sprintf("Rotating %s.%s->%s...", m.pendingNamespace, m.pendingSecretName, m.pendingKey)
				return m, tea.Batch(m.spinner.Tick, runRotateSecretValueCmd(m.selected, m.pendingNamespace, m.pendingSecretName, m.pendingKey, m.pendingStrategy, m.pendingUseRandom, m.pendingUserValue, false))
			}
		case "n", "esc":
			if m.pendingAction == actionRotateSecretValue {
				m.screen = screenK8KeyDetail
			} else {
				m.screen = screenActionMenu
			}
		}
		return m, nil

	case screenOutput:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenTargetList
			return m, nil
		case "s":
			path, err := saveReport(m.selected, m.pendingAction, m.lastOutput)
			if err != nil {
				m.statusMsg = "save failed: " + err.Error()
			} else {
				m.statusMsg = "saved to " + path
			}
			return m, nil
		case "v":
			// Only safe to "re-run on toggle" for a read-only action.
			// Never wire this up for backup/restore/rotate - re-running
			// those from a stray keypress would be a real foot-gun.
			if m.selected.Type == config.TypeK8s && m.pendingAction == actionInventory {
				m.reveal = !m.reveal
				m.screen = screenRunning
				return m, tea.Batch(m.spinner.Tick, runActionCmd(m.selected, actionInventory, "", "", "", m.reveal))
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}

	case screenError:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		default:
			m.screen = screenTargetList
			return m, nil
		}
	}
	return m, nil
}

// focusAddInput blurs every add-target field and focuses index i.
func (m *model) focusAddInput(i int) tea.Cmd {
	for j := range m.addInputs {
		m.addInputs[j].Blur()
	}
	m.addFocus = i
	if i >= 0 && i < len(m.addInputs) {
		m.addInputs[i].Focus()
	}
	return textinput.Blink
}

// buildAddInputs (re)builds the add/edit-target form fields for the given
// type, pre-filled from prefill (a zero-value Target for "add new").
func (m *model) buildAddInputs(prefill config.Target) {
	switch m.addType {
	case config.TypeMySQL, config.TypePsql:
		m.addLabels = []string{"Name", "Host", "Port", "Username", "Notes"}
		m.addInputs = make([]textinput.Model, 5)
		m.addInputs[0] = newTextInput("e.g. web1")
		m.addInputs[1] = newTextInput("127.0.0.1")
		m.addInputs[2] = newTextInput(defaultPortFor(m.addType))
		m.addInputs[3] = newTextInput("root")
		m.addInputs[4] = newTextInput("optional note")
		m.addInputs[0].SetValue(prefill.Name)
		m.addInputs[1].SetValue(prefill.Host)
		if prefill.Port != 0 {
			m.addInputs[2].SetValue(strconv.Itoa(prefill.Port))
		}
		m.addInputs[3].SetValue(prefill.Username)
		m.addInputs[4].SetValue(prefill.Notes)
	case config.TypeK8s:
		m.addLabels = []string{"Name", "Kubeconfig Path (blank = auto-detect)", "Notes"}
		m.addInputs = make([]textinput.Model, 3)
		m.addInputs[0] = newTextInput("e.g. k8s-prod")
		m.addInputs[1] = newTextInput("~/.kube/config")
		m.addInputs[2] = newTextInput("optional note")
		m.addInputs[0].SetValue(prefill.Name)
		m.addInputs[1].SetValue(prefill.KubeconfigPath)
		m.addInputs[2].SetValue(prefill.Notes)
	}
	m.addErr = ""
	m.addFocus = 0
	if len(m.addInputs) > 0 {
		m.addInputs[0].Focus()
	}
}

func defaultPortFor(t config.TargetType) string {
	switch t {
	case config.TypeMySQL:
		return "3306"
	case config.TypePsql:
		return "5432"
	default:
		return ""
	}
}

// submitAddTarget validates the current form and saves it. Returns
// (true, "") on success or (false, message) on validation failure.
func (m *model) submitAddTarget() (bool, string) {
	name := strings.TrimSpace(m.addInputs[0].Value())
	if name == "" {
		return false, "Name is required"
	}

	t := config.Target{Name: name, Type: m.addType}

	switch m.addType {
	case config.TypeMySQL, config.TypePsql:
		host := strings.TrimSpace(m.addInputs[1].Value())
		if host == "" {
			host = "127.0.0.1"
		}
		t.Host = host

		portStr := strings.TrimSpace(m.addInputs[2].Value())
		if portStr == "" {
			t.Port = 0 // resolved to the type default at run time
		} else {
			p, err := strconv.Atoi(portStr)
			if err != nil {
				return false, "Port must be a number"
			}
			t.Port = p
		}
		t.Username = strings.TrimSpace(m.addInputs[3].Value())
		t.Notes = strings.TrimSpace(m.addInputs[4].Value())
	case config.TypeK8s:
		t.KubeconfigPath = strings.TrimSpace(m.addInputs[1].Value())
		t.Notes = strings.TrimSpace(m.addInputs[2].Value())
	}

	if err := config.AddTarget(t); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// ---------------------------------------------------------------------
// View
// ---------------------------------------------------------------------

func (m model) View() string {
	switch m.screen {
	case screenTargetList:
		return m.viewTargetList()
	case screenAddTargetType:
		return m.viewAddTargetType()
	case screenAddTargetFields:
		return m.viewAddTargetFields()
	case screenConfirmDelete:
		return m.viewConfirmDelete()
	case screenActionMenu:
		return m.viewActionMenu()
	case screenFilePrompt:
		return m.viewFilePrompt()
	case screenPasswordPrompt:
		return m.viewPasswordPrompt()
	case screenSequenceInput:
		return m.viewSequenceInput()
	case screenConfirmAction:
		return m.viewConfirmAction()
	case screenK8NamespaceList:
		return m.viewK8NamespaceList()
	case screenK8SecretList:
		return m.viewK8SecretList()
	case screenK8KeyList:
		return m.viewK8KeyList()
	case screenK8KeyDetail:
		return m.viewK8KeyDetail()
	case screenK8Strategy:
		return m.viewK8Strategy()
	case screenK8UserValueInput:
		return m.viewK8UserValueInput()
	case screenRunning:
		return m.viewRunning()
	case screenOutput:
		return m.viewOutput()
	case screenError:
		return m.viewError()
	}
	return ""
}

func (m model) viewTargetList() string {
	s := titleStyle.Render("ccdc-cli dashboard") + "\n\n"

	if len(m.targets) == 0 {
		s += normalStyle.Render("No saved targets yet.") + "\n\n"
		s += helpStyle.Render("Press 'a' to add one right here - no other terminal needed.")
		s += "\n\n" + helpStyle.Render("a: add target   q: quit")
		return s
	}

	for i, t := range m.targets {
		line := fmt.Sprintf("%-20s %s", t.Name, typeTagStyle.Render("["+string(t.Type)+"]"))
		if t.Notes != "" {
			line += "  " + helpStyle.Render(t.Notes)
		}
		if i == m.cursor {
			s += selectedStyle.Render("> "+line) + "\n"
		} else {
			s += normalStyle.Render("  "+line) + "\n"
		}
	}

	s += "\n" + helpStyle.Render("↑/↓: move   enter: actions   a: add   e: edit   d: delete   q: quit")
	return s
}

func (m model) viewAddTargetType() string {
	s := titleStyle.Render("Add target - choose type") + "\n\n"
	labels := map[config.TargetType]string{
		config.TypeMySQL: "mysql",
		config.TypePsql:  "psql",
		config.TypeK8s:   "k8 (Kubernetes)",
	}
	for i, t := range addableTypes {
		line := labels[t]
		if i == m.addTypeCursor {
			s += selectedStyle.Render("> "+line) + "\n"
		} else {
			s += normalStyle.Render("  "+line) + "\n"
		}
	}
	s += "\n" + helpStyle.Render("↑/↓: move   enter: choose   esc: cancel")
	return s
}

func (m model) viewAddTargetFields() string {
	s := titleStyle.Render("Add / edit target") + "\n\n"
	for i, label := range m.addLabels {
		marker := "  "
		if i == m.addFocus {
			marker = "> "
		}
		s += marker + labelStyle.Render(label+":") + "\n"
		s += "    " + m.addInputs[i].View() + "\n"
	}
	if m.addErr != "" {
		s += "\n" + errorStyle.Render(m.addErr) + "\n"
	}
	s += "\n" + helpStyle.Render("tab/↓: next field   shift+tab/↑: prev field   enter: next / save on last field   esc: cancel")
	return s
}

func (m model) viewConfirmDelete() string {
	s := titleStyle.Render("Confirm delete") + "\n\n"
	s += fmt.Sprintf("Remove saved target %q? This only removes the saved connection info, nothing on the target itself.\n\n", m.deleteTarget.Name)
	s += helpStyle.Render("y: delete   n/esc: cancel")
	return s
}

func (m model) viewActionMenu() string {
	s := titleStyle.Render("ccdc-cli dashboard") + "\n\n"
	s += fmt.Sprintf("Target: %s [%s]\n\n", m.selected.Name, m.selected.Type)
	for i, a := range m.actions {
		line := a.label()
		if i == m.actionCursor {
			s += selectedStyle.Render("> "+line) + "\n"
		} else {
			s += normalStyle.Render("  "+line) + "\n"
		}
	}
	s += "\n" + helpStyle.Render("↑/↓: move   enter: run   esc/b: back")
	return s
}

func (m model) viewFilePrompt() string {
	s := titleStyle.Render("ccdc-cli dashboard") + "\n\n"
	label := "Backup file path:"
	if m.pendingAction == actionRestore {
		label = "Restore from file:"
	}
	s += fmt.Sprintf("Target: %s   Action: %s\n\n", m.selected.Name, m.pendingAction.label())
	s += labelStyle.Render(label) + "\n"
	s += m.fileInput.View() + "\n"
	if m.fileErr != "" {
		s += "\n" + errorStyle.Render(m.fileErr) + "\n"
	}
	s += "\n" + helpStyle.Render("enter: continue   esc: back")
	return s
}

func (m model) viewPasswordPrompt() string {
	s := titleStyle.Render("ccdc-cli dashboard") + "\n\n"
	s += fmt.Sprintf("Target: %s (%s@%s:%d)   Action: %s\n\n",
		m.selected.Name, m.selected.Username, m.selected.Host, m.selected.Port, m.pendingAction.label())
	s += m.passwordInput.View() + "\n\n"
	s += helpStyle.Render("enter: continue   esc: back")
	return s
}

func (m model) viewSequenceInput() string {
	s := titleStyle.Render("ccdc-cli dashboard") + "\n\n"
	s += fmt.Sprintf("Target: %s   Action: Roll Credentials\n\n", m.selected.Name)
	s += helpStyle.Render("Format: SECRET_NAME,NAMESPACE,KEY,STRATEGY[|SECRET_NAME,NAMESPACE,KEY,STRATEGY]") + "\n"
	s += helpStyle.Render("STRATEGY is retainPrev (keeps old value under KEY_PREV) or omitPrev") + "\n\n"
	s += m.sequenceInput.View() + "\n"
	if m.sequenceErr != "" {
		s += "\n" + errorStyle.Render(m.sequenceErr) + "\n"
	}
	s += "\n" + helpStyle.Render("enter: continue   esc: back")
	return s
}

func (m model) viewConfirmAction() string {
	s := titleStyle.Render("Confirm") + "\n\n"
	s += warnStyle.Render(m.confirmMessage) + "\n\n"
	s += helpStyle.Render("y: confirm   n/esc: cancel")
	return s
}

func (m model) viewK8NamespaceList() string {
	s := titleStyle.Render("Browse secrets - "+m.selected.Name) + "\n\n"
	if len(m.k8Namespaces) == 0 {
		s += normalStyle.Render("No namespaces found.") + "\n\n"
		s += helpStyle.Render("esc/b: back   q: quit")
		return s
	}
	for i, ns := range m.k8Namespaces {
		line := ns
		if ns == "kube-system" || ns == "kube-public" {
			line += "  " + typeTagStyle.Render("[system]")
		}
		if i == m.k8NamespaceCursor {
			s += selectedStyle.Render("> "+line) + "\n"
		} else {
			s += normalStyle.Render("  "+line) + "\n"
		}
	}
	s += "\n" + helpStyle.Render("↑/↓: move   enter: browse secrets   esc/b: back   q: quit")
	return s
}

func (m model) viewK8SecretList() string {
	s := titleStyle.Render("Browse secrets - "+m.selected.Name+" / "+m.k8Namespace) + "\n\n"
	if len(m.k8Secrets) == 0 {
		s += normalStyle.Render("No secrets in this namespace.") + "\n\n"
		s += helpStyle.Render("esc/b: back   q: quit")
		return s
	}
	for i, sec := range m.k8Secrets {
		line := fmt.Sprintf("%-35s %s", sec.Name, typeTagStyle.Render(sec.Tag))
		if i == m.k8SecretCursor {
			s += selectedStyle.Render("> "+line) + "\n"
		} else {
			s += normalStyle.Render("  "+line) + "\n"
		}
	}
	s += "\n" + helpStyle.Render("↑/↓: move   enter: view keys   esc/b: back   q: quit")
	if m.statusMsg != "" {
		s += "\n" + statusStyle.Render(m.statusMsg)
	}
	return s
}

func (m model) viewK8KeyList() string {
	s := titleStyle.Render("Browse secrets - "+m.k8Namespace+" / "+m.k8SecretName) + "\n\n"
	for i, key := range m.k8Keys {
		if i == m.k8KeyCursor {
			s += selectedStyle.Render("> "+key) + "\n"
		} else {
			s += normalStyle.Render("  "+key) + "\n"
		}
	}
	s += "\n" + helpStyle.Render("↑/↓: move   enter: view/rotate   esc/b: back   q: quit")
	return s
}

func (m model) viewK8KeyDetail() string {
	s := titleStyle.Render(fmt.Sprintf("%s / %s / %s", m.k8Namespace, m.k8SecretName, m.k8SelectedKey)) + "\n\n"
	s += labelStyle.Render("Current value:") + "\n"
	if m.k8ValueRevealed {
		s += m.k8CurrentValue + "\n\n"
	} else {
		s += utils.Redact(m.k8CurrentValue) + "\n\n"
	}
	s += helpStyle.Render("v: toggle reveal   r: rotate to random   u: rotate to user-defined value   esc/b: back   q: quit")
	return s
}

func (m model) viewK8Strategy() string {
	s := titleStyle.Render("Choose rotation strategy") + "\n\n"
	s += fmt.Sprintf("Target: %s / %s / %s\n\n", m.k8Namespace, m.k8SecretName, m.k8SelectedKey)
	options := []string{
		"retainPrev - keep the old value under " + m.k8SelectedKey + "_PREV",
		"omitPrev   - overwrite only, don't retain the old value",
	}
	for i, o := range options {
		if i == m.k8StrategyCursor {
			s += selectedStyle.Render("> "+o) + "\n"
		} else {
			s += normalStyle.Render("  "+o) + "\n"
		}
	}
	s += "\n" + helpStyle.Render("↑/↓: move   enter: choose   esc/b: back")
	return s
}

func (m model) viewK8UserValueInput() string {
	s := titleStyle.Render("Set new value") + "\n\n"
	s += fmt.Sprintf("Target: %s / %s / %s\n\n", m.k8Namespace, m.k8SecretName, m.k8SelectedKey)
	s += m.k8UserValueInput.View() + "\n"
	if m.k8UserValueErr != "" {
		s += "\n" + errorStyle.Render(m.k8UserValueErr) + "\n"
	}
	s += "\n" + helpStyle.Render("enter: continue   esc: back")
	return s
}

func (m model) viewRunning() string {
	s := titleStyle.Render("ccdc-cli dashboard") + "\n\n"
	label := m.runningLabel
	if label == "" {
		label = fmt.Sprintf("Running %s against %s...", strings.ToLower(m.pendingAction.label()), m.selected.Name)
	}
	s += fmt.Sprintf("%s %s\n", m.spinner.View(), label)
	return s
}

func (m model) viewOutput() string {
	header := fmt.Sprintf("%s  [%s]  %s", m.selected.Name, m.selected.Type, m.pendingAction.label())
	s := titleStyle.Render(header) + "\n"
	s += m.viewport.View() + "\n"

	help := "↑/↓/pgup/pgdn: scroll   s: save report   esc/b: back   q: quit"
	if m.selected.Type == config.TypeK8s && m.pendingAction == actionInventory {
		if m.reveal {
			help = "v: hide secrets   " + help
		} else {
			help = "v: reveal secrets   " + help
		}
	}
	s += helpStyle.Render(help)
	if m.statusMsg != "" {
		s += "\n" + statusStyle.Render(m.statusMsg)
	}
	return s
}

func (m model) viewError() string {
	s := titleStyle.Render("ccdc-cli dashboard") + "\n\n"
	s += errorStyle.Render("Error: "+m.errMsg) + "\n\n"
	s += helpStyle.Render("any key: back   q: quit")
	return s
}

// ---------------------------------------------------------------------
// Action dispatch
// ---------------------------------------------------------------------

func loadNamespacesCmd(kubeconfigPath string) tea.Cmd {
	return func() tea.Msg {
		ns, err := k8Module.ListNamespaces(kubeconfigPath)
		return namespacesLoadedMsg{namespaces: ns, err: err}
	}
}

func loadSecretsCmd(kubeconfigPath, namespace string) tea.Cmd {
	return func() tea.Msg {
		secrets, err := k8Module.ListSecrets(kubeconfigPath, namespace)
		return secretsLoadedMsg{secrets: secrets, err: err}
	}
}

func loadKeyValueCmd(kubeconfigPath, namespace, secretName, key string) tea.Cmd {
	return func() tea.Msg {
		v, err := k8Module.GetSecretKeyValue(kubeconfigPath, namespace, secretName, key)
		return keyValueLoadedMsg{value: v, err: err}
	}
}

func runRotateSecretValueCmd(t config.Target, namespace, secretName, key, strategy string, useRandom bool, userValue string, reveal bool) tea.Cmd {
	return func() tea.Msg {
		out, err := k8Module.RotateSecretValueCapture(t.KubeconfigPath, namespace, secretName, key, strategy, useRandom, userValue, reveal)
		return inventoryResultMsg{output: out, err: err}
	}
}

func runActionCmd(t config.Target, action actionType, password string, filePath string, sequence string, reveal bool) tea.Cmd {
	return func() tea.Msg {
		var out string
		var err error

		switch t.Type {
		case config.TypeMySQL:
			port := t.Port
			if port == 0 {
				port = 3306
			}
			switch action {
			case actionInventory:
				out, err = mysqlModule.RunInventoryCapture(t.Host, port, t.Username, password)
			case actionBackup:
				out, err = mysqlModule.RunBackupCapture(t.Host, port, t.Username, password, filePath)
			case actionRestore:
				out, err = mysqlModule.RunRestoreCapture(t.Host, port, t.Username, password, filePath)
			default:
				err = fmt.Errorf("action %q not supported for mysql targets", action.label())
			}

		case config.TypePsql:
			port := t.Port
			if port == 0 {
				port = 5432
			}
			switch action {
			case actionInventory:
				out, err = psqlModule.RunInventoryCapture(t.Host, port, t.Username, password)
			case actionBackup:
				out, err = psqlModule.RunBackupCapture(t.Host, port, t.Username, password, filePath)
			case actionRestore:
				out, err = psqlModule.RunRestoreCapture(t.Host, port, t.Username, password, filePath)
			default:
				err = fmt.Errorf("action %q not supported for psql targets", action.label())
			}

		case config.TypeK8s:
			switch action {
			case actionInventory:
				out, err = k8Module.RunInventoryCapture(t.KubeconfigPath, reveal)
			case actionRollCredentials:
				out, err = k8Module.RollCredentialsCapture(t.KubeconfigPath, sequence, reveal)
			default:
				err = fmt.Errorf("action %q not supported for k8 targets", action.label())
			}

		default:
			err = fmt.Errorf("unknown target type %q", t.Type)
		}

		return inventoryResultMsg{output: out, err: err}
	}
}

func saveReport(t config.Target, action actionType, output string) (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	reportsDir := filepath.Join(dir, "reports")
	if err := os.MkdirAll(reportsDir, 0o700); err != nil {
		return "", err
	}

	safeAction := strings.ToLower(strings.ReplaceAll(action.label(), " ", "-"))
	filename := fmt.Sprintf("%s-%s-%s.txt", t.Name, safeAction, time.Now().Format("20060102-150405"))
	path := filepath.Join(reportsDir, filename)

	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
