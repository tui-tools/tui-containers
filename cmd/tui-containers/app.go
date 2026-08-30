package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// screen is one of the four views the tool is made of. They are tabs rather
// than nested screens because they answer four separate questions about the
// same machine, and a reader arrives with one of them already in mind.
type screen int

const (
	// screenContainers is the merged list, which is the reason the tool exists.
	screenContainers screen = iota
	// screenImages is what is in the stores and what it costs.
	screenImages
	// screenStorage is the volumes and the networks, which are the two things
	// a container leaves behind and nobody goes looking for.
	screenStorage
	// screenSystem is what each engine says about itself.
	screenSystem
	screenCount
)

// title names a screen for the tab bar.
func (s screen) title() string {
	switch s {
	case screenImages:
		return "images"
	case screenStorage:
		return "volumes & networks"
	case screenSystem:
		return "engines"
	default:
		return "containers"
	}
}

// mode is the dialog or pane the app currently has open. Only one is open at a
// time, which keeps the update loop flat.
type mode int

const (
	modeBrowse mode = iota
	modeDetail
	modeLogs
	modeConfirm
	modeFilter
	modePicker
	modeHelp
)

// pickerKind is what an open picker is choosing, so the answer can be routed
// back to the action that asked.
type pickerKind int

const (
	pickerNone pickerKind = iota
	// pickerRemove chooses between a plain removal and a forced one.
	pickerRemove
	// pickerPolicy chooses a restart policy.
	pickerPolicy
	// pickerCompose chooses a Compose verb for the selected container's
	// project.
	pickerCompose
	// pickerPrune chooses which prune to preview, with each variant spelled
	// out rather than hidden behind a flag.
	pickerPrune
)

// tailSteps are the log window sizes the pane cycles through.
var tailSteps = []int{50, 200, 1000}

// sinceSteps are the time windows the log pane cycles through. The empty one
// means "whatever the tail covers", which is the default and the honest one:
// a container that logged nothing for a day has no lines in the last hour and
// that is not the same as having no log.
var sinceSteps = []string{"", "15m", "1h", "24h"}

// followInterval is how often the log pane re-reads while following.
//
// It re-reads rather than following a stream, because `docker logs -f` is a
// process that never returns and this tool starts none: every command it runs
// is one it waits for and one the user could have run themselves. Two seconds
// is close enough to live for a log, and it costs one invocation.
const followInterval = 2 * time.Second

// app is the tui-containers Bubble Tea model.
type app struct {
	backend container.Backend
	theme   theme.Theme
	caps    container.Capabilities
	// backendCompat is what the version probes found, rendered in the header.
	backendCompat []compat.Result

	model container.Model

	// The rows left after the filter, per screen, in display order.
	containers []container.Container
	images     []container.Image
	storage    []storageRow
	system     []systemRow

	width, height int
	screen        screen
	// cursor and offset are per screen, so moving between tabs does not lose
	// the row the reader was on.
	cursor [screenCount]int
	offset [screenCount]int
	filter string

	// detail is what the per-row screen shows, loaded in the background
	// because it is a read against the machine.
	detail       container.Detail
	detailFor    string
	detailErr    string
	detailLoad   bool
	detailOffset int

	// logs is the pane's state: which container, what was read, and how.
	logs       string
	logsFor    string
	logsErr    string
	logsOpts   container.LogOptions
	logsOffset int
	// logsFollow re-reads on a timer; logsTick guards against two timers
	// running after the pane is reopened.
	logsFollow bool
	logsTick   int
	// logsPinned keeps the view at the bottom as new lines arrive, which is
	// what a reader following a log means by following it.
	logsPinned bool

	mode    mode
	confirm ui.Confirm
	input   ui.Input
	picker  ui.Picker
	// pending is what an open picker will act on once it is answered.
	pickerKind pickerKind
	pendingC   container.Container
	pendingT   container.Target

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last Load returned an error, so the empty
	// state does not claim the machine simply has nothing running.
	loadFailed bool
	// busy blocks input while a command runs.
	busy bool
}

// storageRow is one line of the volumes and networks screen: either a volume
// or a network, kept in one list so the screen scrolls and filters as one.
type storageRow struct {
	volume  *container.Volume
	network *container.Network
}

// systemRow is one line of the engines screen: a fact and its value, with the
// engine it belongs to so an action on that screen knows its target.
type systemRow struct {
	label  string
	value  string
	target container.Target
	// warn marks a line that is a finding rather than a fact.
	warn bool
}

// loadedMsg carries the result of a Load.
type loadedMsg struct {
	model container.Model
	err   error
}

// detailMsg carries the result of an Inspect.
type detailMsg struct {
	// id is the container the read was for, so a slow answer for a row the
	// reader has already left is discarded rather than shown under the wrong
	// title.
	id     string
	detail container.Detail
	err    error
}

// logsMsg carries the result of a log read.
type logsMsg struct {
	id   string
	text string
	err  error
}

// tickMsg wakes the log pane for its next re-read.
type tickMsg struct{ seq int }

// ranMsg carries the result of running an action.
type ranMsg struct {
	title  string
	output string
	err    error
}

// newApp builds the model around a backend.
func newApp(backend container.Backend, th theme.Theme,
	backendCompat []compat.Result) *app {
	a := &app{
		backend:       backend,
		theme:         th,
		caps:          backend.Capabilities(),
		backendCompat: backendCompat,
		width:         80,
		height:        24,
		loading:       true,
		logsOpts:      container.LogOptions{Tail: 200, Timestamps: true},
		logsPinned:    true,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first load.
func (a *app) Init() tea.Cmd { return a.load() }

// load reads every engine in the background.
func (a *app) load() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		model, err := backend.Load(ctx)
		return loadedMsg{model: model, err: err}
	}
}

// loadDetail inspects one container in the background.
//
// It is read on opening the row rather than with the list, because it is a
// process per container: doing it for every row on every reload would turn a
// list of sixty containers into sixty commands.
func (a *app) loadDetail(c container.Container) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		detail, err := backend.Inspect(ctx, c)
		return detailMsg{id: c.ID, detail: detail, err: err}
	}
}

// loadLogs reads the end of a container's log in the background.
func (a *app) loadLogs(c container.Container, opts container.LogOptions) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := backend.Logs(ctx, c, opts)
		return logsMsg{id: c.ID, text: text, err: err}
	}
}

// tick schedules the log pane's next re-read.
func (a *app) tick() tea.Cmd {
	seq := a.logsTick
	return tea.Tick(followInterval, func(time.Time) tea.Msg {
		return tickMsg{seq: seq}
	})
}

// run executes a confirmed action in the background, one command at a time,
// stopping at the first failure.
func (a *app) run(action container.Action) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		var outputs []string
		for _, cmd := range action.Commands {
			out, err := backend.Run(ctx, action.Target, cmd)
			if err != nil {
				return ranMsg{title: action.Title, output: out, err: err}
			}
			if trimmed := strings.TrimSpace(out); trimmed != "" {
				outputs = append(outputs, trimmed)
			}
		}
		return ranMsg{title: action.Title, output: strings.Join(outputs, "; ")}
	}
}

// setStatus records a plain message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case loadedMsg:
		a.loading = false
		if msg.err != nil {
			a.loadFailed = true
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.loadFailed = false
		a.model = msg.model
		a.applyFilter()
		return a, nil

	case detailMsg:
		if msg.id != a.detailFor {
			// The reader moved on before the answer arrived.
			return a, nil
		}
		a.detailLoad = false
		if msg.err != nil {
			a.detailErr = msg.err.Error()
			return a, nil
		}
		a.detailErr, a.detail = "", msg.detail
		return a, nil

	case logsMsg:
		if msg.id != a.logsFor {
			return a, nil
		}
		if msg.err != nil {
			a.logsErr = msg.err.Error()
		} else {
			a.logsErr, a.logs = "", msg.text
		}
		if a.logsPinned {
			a.logsOffset = a.logsBottom()
		}
		return a, nil

	case tickMsg:
		// A tick from a pane that has since been closed or reopened is
		// ignored, which is what keeps two timers from ever running at once.
		if msg.seq != a.logsTick || a.mode != modeLogs || !a.logsFollow {
			return a, nil
		}
		c, ok := a.selectedContainer()
		if !ok {
			return a, nil
		}
		return a, tea.Batch(a.loadLogs(c, a.logsOpts), a.tick())

	case ranMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, a.load()
		}
		summary := strings.TrimSpace(msg.output)
		if summary == "" {
			summary = "done"
		}
		a.setStatusf(ui.StatusOK, "%s: %s", msg.title, firstLine(summary))
		a.loading = true
		return a, a.load()

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeFilter {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// handleKey routes a key press to the open dialog, or to the current screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	if a.busy {
		// A command is running: swallow input rather than queueing surprises.
		return a, nil
	}

	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeFilter:
		return a.handleFilter(msg)
	case modePicker:
		return a.handlePicker(msg)
	case modeHelp:
		a.mode = modeBrowse
		return a, nil
	case modeLogs:
		return a.handleLogsKey(msg)
	case modeDetail:
		return a.handleDetailKey(msg)
	default:
		return a.handleBrowseKey(msg)
	}
}

// handleConfirm resolves the confirm dialog.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = modeBrowse
	confirmed := a.confirm.Confirmed
	pending, ok := a.confirm.Payload.(container.Action)
	a.confirm = ui.Confirm{}
	if !confirmed || !ok {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…",
		a.backend.Preview(pending.Target, pending.Commands[0]))
	return a, a.run(pending)
}

// handleFilter resolves the filter prompt.
func (a *app) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		// Filter as the user types.
		a.filter = a.input.Value()
		a.applyFilter()
		return a, cmd
	}
	if a.input.Accepted {
		a.filter = a.input.Value()
	} else {
		a.filter = ""
	}
	a.applyFilter()
	a.mode = modeBrowse
	return a, nil
}

// handlePicker resolves the open picker and builds the action it chose.
func (a *app) handlePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.picker.Update(msg)
	if !a.picker.Done {
		return a, nil
	}
	choice, accepted := a.picker.Selected(), a.picker.Accepted
	kind := a.pickerKind
	a.picker, a.pickerKind = ui.Picker{}, pickerNone
	a.mode = modeBrowse
	if !accepted {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}

	var action container.Action
	var err error
	switch kind {
	case pickerRemove:
		action, err = a.backend.BuildRemove(a.pendingC,
			strings.HasPrefix(choice, "force"))
	case pickerPolicy:
		action, err = a.backend.BuildUpdateRestart(a.pendingC, choice)
	case pickerCompose:
		project, ok := a.model.Project(a.pendingC.Project, a.pendingC.Target)
		if !ok {
			a.setStatusf(ui.StatusWarn, "%s is not on this machine any more",
				a.pendingC.Project)
			return a, nil
		}
		action, err = a.backend.BuildCompose(project, choice)
	case pickerPrune:
		action, err = a.pruneFor(choice)
	default:
		return a, nil
	}
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return a, nil
	}
	a.openConfirm(action)
	return a, nil
}

// pruneOptions is the closed list the prune picker offers, with each variant
// spelled out rather than hidden behind a flag. A reader chooses a sentence;
// the dialog then shows the command line it produced.
var pruneOptions = []string{
	"dangling images only",
	"every image no container uses (-a)",
	"unused volumes — deletes data",
	"unused networks",
	"system prune: stopped containers, dangling images, unused networks",
	"system prune -a --volumes: also every unused image and volume",
}

// pruneFor turns a chosen sentence into an action.
func (a *app) pruneFor(choice string) (container.Action, error) {
	switch choice {
	case pruneOptions[0]:
		return a.backend.BuildPruneImages(a.pendingT, false)
	case pruneOptions[1]:
		return a.backend.BuildPruneImages(a.pendingT, true)
	case pruneOptions[2]:
		return a.backend.BuildPruneVolumes(a.pendingT)
	case pruneOptions[3]:
		return a.backend.BuildPruneNetworks(a.pendingT)
	case pruneOptions[4]:
		return a.backend.BuildSystemPrune(a.pendingT, container.PruneOptions{})
	case pruneOptions[5]:
		return a.backend.BuildSystemPrune(a.pendingT,
			container.PruneOptions{All: true, Volumes: true})
	default:
		return container.Action{}, fmt.Errorf("%q is not a prune this tool offers",
			choice)
	}
}

// handleBrowseKey handles a screen's own keys.
func (a *app) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
	case "j", "down":
		a.moveCursor(1)
	case "k", "up":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor[a.screen], a.offset[a.screen] = 0, 0
	case "G", "end":
		a.cursor[a.screen] = max(a.rowCount()-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.tableHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.tableHeight())
	case "tab", "l", "right":
		a.gotoScreen((a.screen + 1) % screenCount)
	case "shift+tab", "h", "left":
		a.gotoScreen((a.screen + screenCount - 1) % screenCount)
	case "1", "2", "3", "4":
		a.gotoScreen(screen(msg.String()[0] - '1'))
	case "/":
		a.input = ui.NewInput("Filter "+a.screen.title(), "any column…", a.filter)
		a.input.Help = "Matches any column of this screen. Empty clears the filter."
		a.mode = modeFilter
	case "enter":
		return a, a.openDetail()
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
	return a, nil
}

// openDetail opens the per-row screen and starts its read.
func (a *app) openDetail() tea.Cmd {
	c, ok := a.selectedContainer()
	if !ok {
		a.setStatus(ui.StatusWarn, "this screen has no container to open")
		return nil
	}
	a.mode, a.detailOffset = modeDetail, 0
	a.detail, a.detailErr = container.Detail{Container: c}, ""
	a.detailFor, a.detailLoad = c.ID, true
	return a.loadDetail(c)
}

// openLogs opens the log pane for the selected container.
func (a *app) openLogs() tea.Cmd {
	c, ok := a.selectedContainer()
	if !ok {
		a.setStatus(ui.StatusWarn, "this screen has no container to read a log for")
		return nil
	}
	a.mode, a.logsOffset = modeLogs, 0
	a.logs, a.logsErr, a.logsFor = "", "", c.ID
	a.logsPinned = true
	// The sequence number moves on every open, so a tick left over from a
	// previous pane is ignored rather than re-reading the wrong container.
	a.logsTick++
	cmds := []tea.Cmd{a.loadLogs(c, a.logsOpts)}
	if a.logsFollow {
		cmds = append(cmds, a.tick())
	}
	return tea.Batch(cmds...)
}

// handleDetailKey handles the per-row screen. The action keys are the same ones
// the table offers, applied to the row on screen.
func (a *app) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "backspace", "left":
		a.mode, a.detailOffset = modeBrowse, 0
		return a, nil
	case "?":
		a.mode = modeHelp
		return a, nil
	case "j", "down":
		a.detailOffset++
		return a, nil
	case "k", "up":
		a.detailOffset = max(a.detailOffset-1, 0)
		return a, nil
	case "g", "home":
		a.detailOffset = 0
		return a, nil
	case "pgdown", "ctrl+f":
		a.detailOffset += a.detailHeight()
		return a, nil
	case "pgup", "ctrl+b":
		a.detailOffset = max(a.detailOffset-a.detailHeight(), 0)
		return a, nil
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
}

// handleLogsKey handles the log pane, whose keys are its own: the pane is where
// a reader is reading rather than acting, and a stray `d` there must not remove
// the container behind it.
func (a *app) handleLogsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "backspace":
		a.mode, a.logsOffset = modeBrowse, 0
		// Closing the pane retires the timer.
		a.logsTick++
		return a, nil
	case "?":
		a.mode = modeHelp
		return a, nil
	case "j", "down":
		a.logsOffset++
		a.logsPinned = a.logsOffset >= a.logsBottom()
		return a, nil
	case "k", "up":
		a.logsOffset, a.logsPinned = max(a.logsOffset-1, 0), false
		return a, nil
	case "g", "home":
		a.logsOffset, a.logsPinned = 0, false
		return a, nil
	case "G", "end":
		a.logsOffset, a.logsPinned = a.logsBottom(), true
		return a, nil
	case "pgdown", "ctrl+f":
		a.logsOffset += a.logsHeight()
		a.logsPinned = a.logsOffset >= a.logsBottom()
		return a, nil
	case "pgup", "ctrl+b":
		a.logsOffset, a.logsPinned = max(a.logsOffset-a.logsHeight(), 0), false
		return a, nil
	case "f":
		a.logsFollow = !a.logsFollow
		a.logsTick++
		if a.logsFollow {
			a.logsPinned = true
			a.setStatusf(ui.StatusInfo, "following: re-reading every %s",
				followInterval)
			return a, a.tick()
		}
		a.setStatus(ui.StatusInfo, "stopped following")
		return a, nil
	case "t":
		a.logsOpts.Timestamps = !a.logsOpts.Timestamps
		return a, a.reloadLogs()
	case "n":
		a.logsOpts.Tail = nextOf(tailSteps, a.logsOpts.Tail)
		a.setStatusf(ui.StatusInfo, "reading the last %d lines", a.logsOpts.Tail)
		return a, a.reloadLogs()
	case "w":
		a.logsOpts.Since = nextString(sinceSteps, a.logsOpts.Since)
		if a.logsOpts.Since == "" {
			a.setStatus(ui.StatusInfo,
				"no time window: whatever the last lines cover")
		} else {
			a.setStatusf(ui.StatusInfo, "the last %s", a.logsOpts.Since)
		}
		return a, a.reloadLogs()
	case "R", "ctrl+r":
		return a, a.reloadLogs()
	}
	return a, nil
}

// reloadLogs re-reads the pane's container with the current options.
func (a *app) reloadLogs() tea.Cmd {
	c, ok := a.selectedContainer()
	if !ok {
		return nil
	}
	return a.loadLogs(c, a.logsOpts)
}

// nextOf cycles through a list of sizes.
func nextOf(steps []int, current int) int {
	for i, step := range steps {
		if step == current {
			return steps[(i+1)%len(steps)]
		}
	}
	return steps[0]
}

// nextString cycles through a list of windows.
func nextString(steps []string, current string) string {
	for i, step := range steps {
		if step == current {
			return steps[(i+1)%len(steps)]
		}
	}
	return steps[0]
}

// handleActionKey handles the keys that mean the same thing on every screen.
//
// Each one refuses in the backend's own words when it does not apply, which is
// how "this container is already running" reaches the status line instead of a
// key that silently does nothing.
func (a *app) handleActionKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "L":
		return a.openLogs()
	case "s":
		return a.confirmContainer("start", a.backend.BuildStart)
	case "x":
		return a.confirmContainer("stop", a.backend.BuildStop)
	case "r":
		return a.confirmContainer("restart", a.backend.BuildRestart)
	case "K":
		return a.confirmContainer("kill", a.backend.BuildKill)
	case "p":
		return a.confirmContainer("pause", a.backend.BuildPause)
	case "u":
		return a.confirmContainer("unpause", a.backend.BuildUnpause)
	case "U":
		return a.confirmContainer("pull", a.backend.BuildPullImage)
	case "d":
		return a.confirmDelete()
	case "o":
		return a.openPolicyPicker()
	case "c":
		return a.openComposePicker()
	case "X":
		return a.openPrunePicker()
	case "A":
		return a.confirmAutoUpdate()
	}
	return nil
}

// confirmContainer builds a container action and opens the confirm dialog, or
// reports the builder's refusal in the status line.
func (a *app) confirmContainer(verb string,
	build func(container.Container) (container.Action, error)) tea.Cmd {
	c, ok := a.selectedContainer()
	if !ok {
		a.setStatusf(ui.StatusWarn, "select a container to %s", verb)
		return nil
	}
	if !a.caps.SupportsLifecycle {
		a.setStatus(ui.StatusWarn, "no engine on this machine can be driven")
		return nil
	}
	action, err := build(c)
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	a.openConfirm(action)
	return nil
}

// confirmDelete removes whatever the current screen is about: a container, an
// image, a volume or a network. It is one key because it is one idea, and each
// screen's builder refuses for its own reasons — an image a container uses, a
// volume something mounts, a network the engine owns.
func (a *app) confirmDelete() tea.Cmd {
	switch a.screen {
	case screenImages:
		image, ok := a.selectedImage()
		if !ok {
			a.setStatus(ui.StatusWarn, "nothing selected")
			return nil
		}
		action, err := a.backend.BuildRemoveImage(image, false)
		if err != nil {
			a.setStatus(ui.StatusWarn, err.Error())
			return nil
		}
		a.openConfirm(action)
		return nil
	case screenStorage:
		return a.confirmDeleteStorage()
	case screenSystem:
		a.setStatus(ui.StatusWarn,
			"nothing on this screen can be removed; X prunes")
		return nil
	}

	c, ok := a.selectedContainer()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	// A running container cannot simply be removed, and the choice between
	// stopping it first and killing it is the user's rather than a flag this
	// tool adds quietly.
	if c.Running() {
		a.pendingC, a.pickerKind = c, pickerRemove
		a.picker = ui.NewPicker(c.Name+" is running", []string{
			"stop it first, then remove it — cancel and press x",
			"force: kill it and remove it now",
		}, "")
		a.mode = modePicker
		return nil
	}
	action, err := a.backend.BuildRemove(c, false)
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	a.openConfirm(action)
	return nil
}

// confirmDeleteStorage removes the selected volume or network.
func (a *app) confirmDeleteStorage() tea.Cmd {
	row, ok := a.selectedStorage()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	var action container.Action
	var err error
	if row.volume != nil {
		action, err = a.backend.BuildRemoveVolume(*row.volume)
	} else {
		action, err = a.backend.BuildRemoveNetwork(*row.network)
	}
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	a.openConfirm(action)
	return nil
}

// openPolicyPicker asks which restart policy the selected container should get.
func (a *app) openPolicyPicker() tea.Cmd {
	c, ok := a.selectedContainer()
	if !ok {
		a.setStatus(ui.StatusWarn, "select a container to change its policy")
		return nil
	}
	if !a.caps.SupportsUpdate {
		a.setStatus(ui.StatusWarn, "no engine on this machine can be driven")
		return nil
	}
	policies := a.caps.RestartPolicies
	if len(policies) == 0 {
		a.setStatus(ui.StatusWarn, "this engine declares no restart policies")
		return nil
	}
	a.pendingC, a.pickerKind = c, pickerPolicy
	a.picker = ui.NewPicker("Restart policy for "+c.Name, policies,
		c.RestartPolicy)
	a.mode = modePicker
	return nil
}

// openComposePicker asks which Compose verb to run for the selected
// container's project.
func (a *app) openComposePicker() tea.Cmd {
	c, ok := a.selectedContainer()
	if !ok {
		a.setStatus(ui.StatusWarn, "select a container in a compose project")
		return nil
	}
	if c.Project == "" {
		a.setStatusf(ui.StatusWarn,
			"%s carries no compose labels, so it is not part of a project",
			c.Name)
		return nil
	}
	if !a.caps.SupportsCompose {
		a.setStatus(ui.StatusWarn,
			"no compose command answered on this machine")
		return nil
	}
	if c.WorkingDir == "" {
		a.setStatusf(ui.StatusWarn,
			"%s carries no working_dir label, so there is no directory to run "+
				"compose in", c.Project)
		return nil
	}
	a.pendingC, a.pickerKind = c, pickerCompose
	a.picker = ui.NewPicker("compose — "+c.Project,
		[]string{"up", "down", "pull"}, "up")
	a.mode = modePicker
	return nil
}

// openPrunePicker asks which prune to preview. Every variant is a sentence
// rather than a flag, because the difference between them is what gets deleted.
func (a *app) openPrunePicker() tea.Cmd {
	target, ok := a.selectedTarget()
	if !ok {
		a.setStatus(ui.StatusWarn, "select a row so the engine is known")
		return nil
	}
	if !a.caps.SupportsPrune {
		a.setStatus(ui.StatusWarn, "no engine on this machine can be driven")
		return nil
	}
	a.pendingT, a.pickerKind = target, pickerPrune
	a.picker = ui.NewPicker("Prune on "+target.String(), pruneOptions, "")
	a.mode = modePicker
	return nil
}

// confirmAutoUpdate previews Podman's auto-update as a dry run.
func (a *app) confirmAutoUpdate() tea.Cmd {
	target, ok := a.selectedTarget()
	if !ok {
		a.setStatus(ui.StatusWarn, "select a row so the engine is known")
		return nil
	}
	action, err := a.backend.BuildAutoUpdate(target)
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	a.openConfirm(action)
	return nil
}

// openConfirm shows an action's command lines and what they do.
func (a *app) openConfirm(action container.Action) {
	if len(action.Commands) == 0 {
		a.setStatus(ui.StatusWarn, "that action has nothing to run")
		return
	}
	body := action.Body
	if action.Warning != "" {
		body += "\n\n" + action.Warning
	}
	body += "\n\non " + action.Target.String()
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   action.Title,
		Body:    strings.TrimSpace(body),
		Command: a.previewAll(action),
		Danger:  action.Destructive || action.Warning != "",
		Payload: action,
	}
}

// previewAll renders every command of an action, one per line, each with the
// prompt the dialog puts in front of the first one.
func (a *app) previewAll(action container.Action) string {
	previews := make([]string, 0, len(action.Commands))
	for _, cmd := range action.Commands {
		previews = append(previews, a.backend.Preview(action.Target, cmd))
	}
	return strings.Join(previews, "\n$ ")
}

// gotoScreen switches tabs, keeping the filter applied.
func (a *app) gotoScreen(next screen) {
	if next < 0 || next >= screenCount {
		return
	}
	a.screen = next
	a.clampCursor()
}

// applyFilter recomputes every screen's visible rows from the current filter.
func (a *app) applyFilter() {
	needle := strings.ToLower(a.filter)
	keep := func(haystack string) bool {
		return needle == "" ||
			strings.Contains(strings.ToLower(haystack), needle)
	}

	a.containers = nil
	for _, c := range a.model.Containers {
		if keep(c.Haystack()) {
			a.containers = append(a.containers, c)
		}
	}
	a.images = nil
	for _, image := range a.model.Images {
		if keep(image.Haystack()) {
			a.images = append(a.images, image)
		}
	}
	a.storage = nil
	for i := range a.model.Volumes {
		if keep(a.model.Volumes[i].Haystack()) {
			a.storage = append(a.storage, storageRow{volume: &a.model.Volumes[i]})
		}
	}
	for i := range a.model.Networks {
		if keep(a.model.Networks[i].Haystack()) {
			a.storage = append(a.storage, storageRow{network: &a.model.Networks[i]})
		}
	}
	a.system = a.buildSystemRows(keep)
	a.clampCursor()
}

// rowCount is how many rows the current screen has after the filter.
func (a *app) rowCount() int {
	switch a.screen {
	case screenImages:
		return len(a.images)
	case screenStorage:
		return len(a.storage)
	case screenSystem:
		return len(a.system)
	default:
		return len(a.containers)
	}
}

// selectedContainer is the highlighted container, and whether there is one.
//
// The detail and log screens keep answering with the row they were opened from,
// so an action key pressed there acts on what is on screen rather than on
// whatever the list behind it has moved to.
func (a *app) selectedContainer() (container.Container, bool) {
	if a.mode == modeDetail && a.detailFor != "" {
		return a.detail.Container, true
	}
	if a.mode == modeLogs && a.logsFor != "" {
		if c, ok := a.model.Container(a.logsFor); ok {
			return c, true
		}
	}
	if a.screen != screenContainers {
		return container.Container{}, false
	}
	index := a.cursor[screenContainers]
	if index < 0 || index >= len(a.containers) {
		return container.Container{}, false
	}
	return a.containers[index], true
}

// selectedImage is the highlighted image on the images screen.
func (a *app) selectedImage() (container.Image, bool) {
	if a.screen != screenImages {
		return container.Image{}, false
	}
	index := a.cursor[screenImages]
	if index < 0 || index >= len(a.images) {
		return container.Image{}, false
	}
	return a.images[index], true
}

// selectedStorage is the highlighted volume or network.
func (a *app) selectedStorage() (storageRow, bool) {
	if a.screen != screenStorage {
		return storageRow{}, false
	}
	index := a.cursor[screenStorage]
	if index < 0 || index >= len(a.storage) {
		return storageRow{}, false
	}
	return a.storage[index], true
}

// selectedTarget is which engine the current row belongs to, which is what an
// engine-wide action such as a prune needs to know.
//
// It comes from the row rather than from a global setting on purpose: on a
// machine with two engines there is no such thing as "the" engine, and a prune
// that guessed would be a prune on the wrong store.
func (a *app) selectedTarget() (container.Target, bool) {
	switch a.screen {
	case screenImages:
		if image, ok := a.selectedImage(); ok {
			return image.Target, true
		}
	case screenStorage:
		if row, ok := a.selectedStorage(); ok {
			if row.volume != nil {
				return row.volume.Target, true
			}
			return row.network.Target, true
		}
	case screenSystem:
		index := a.cursor[screenSystem]
		if index >= 0 && index < len(a.system) {
			return a.system[index].target, true
		}
	default:
		if c, ok := a.selectedContainer(); ok {
			return c.Target, true
		}
	}
	return container.Target{}, false
}

// moveCursor moves the selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor[a.screen] += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset of every screen in range.
func (a *app) clampCursor() {
	for s := screen(0); s < screenCount; s++ {
		count := a.countFor(s)
		if count == 0 {
			a.cursor[s], a.offset[s] = 0, 0
			continue
		}
		a.cursor[s] = min(max(a.cursor[s], 0), count-1)

		height := a.tableHeight()
		if a.cursor[s] < a.offset[s] {
			a.offset[s] = a.cursor[s]
		}
		if a.cursor[s] >= a.offset[s]+height {
			a.offset[s] = a.cursor[s] - height + 1
		}
		a.offset[s] = max(min(a.offset[s], max(count-height, 0)), 0)
	}
}

// countFor is rowCount for a screen that is not the current one.
func (a *app) countFor(s screen) int {
	current := a.screen
	a.screen = s
	count := a.rowCount()
	a.screen = current
	return count
}

// firstLine keeps status messages to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
