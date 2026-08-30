package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-containers/internal/engines"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// newTestApp builds the app on the sample machine, loaded, at a size a
// terminal really is.
func newTestApp(t *testing.T) (*app, *engines.Fake) {
	t.Helper()
	backend := engines.NewFake()
	a := newApp(backend, theme.New(), nil)
	a.width, a.height = 120, 32
	model, err := backend.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a.Update(loadedMsg{model: model})
	if len(a.containers) == 0 {
		t.Fatal("the sample machine loaded with no containers")
	}
	return a, backend
}

// press sends one key to the app.
func press(a *app, key string) {
	if len(key) == 1 {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		return
	}
	switch key {
	case "enter":
		a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	case "down":
		a.Update(tea.KeyMsg{Type: tea.KeyDown})
	case "tab":
		a.Update(tea.KeyMsg{Type: tea.KeyTab})
	default:
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	}
}

// selectByName moves the cursor to a named container on the containers screen.
func selectByName(t *testing.T, a *app, name string) container.Container {
	t.Helper()
	for i, c := range a.containers {
		if c.Name == name {
			a.screen = screenContainers
			a.cursor[screenContainers] = i
			return c
		}
	}
	t.Fatalf("the sample machine has no container called %q", name)
	return container.Container{}
}

// TestEveryScreenRendersAtEveryWidth is the layout guard the whole family
// shares. A row wider than the terminal does more than look wrong: it
// desynchronises Bubble Tea's line accounting, so every frame after it is drawn
// in the wrong place.
//
// It walks every screen, every pane and every dialog at widths from a narrow
// side pane to a full-screen terminal, and asserts that no rendered line is
// wider than the terminal it was rendered for.
func TestEveryScreenRendersAtEveryWidth(t *testing.T) {
	widths := []int{40, 56, 72, 80, 100, 120, 160, 200}
	heights := []int{12, 24, 32, 50}

	for _, width := range widths {
		for _, height := range heights {
			for s := screen(0); s < screenCount; s++ {
				a, _ := newTestApp(t)
				a.width, a.height = width, height
				a.screen = s
				a.clampCursor()
				checkFrame(t, a.View(), width, "screen "+s.title())

				// The detail pane and the log pane, which are the two views
				// that render text rather than a table.
				if s == screenContainers {
					a.openDetail()
					checkFrame(t, a.View(), width, "detail")
					a.mode = modeBrowse
					a.openLogs()
					checkFrame(t, a.View(), width, "logs")
					a.mode = modeBrowse
				}
			}
		}
	}
}

// TestDialogsRenderAtEveryWidth covers the four overlays, which are placed
// rather than laid out and so fail differently from a table.
func TestDialogsRenderAtEveryWidth(t *testing.T) {
	for _, width := range []int{40, 56, 80, 120, 200} {
		a, _ := newTestApp(t)
		a.width, a.height = width, 32

		a.mode = modeHelp
		checkFrame(t, a.View(), width, "help")

		selectByName(t, a, "shopfront-api")
		a.mode = modeBrowse
		press(a, "x")
		if a.mode != modeConfirm {
			t.Fatalf("stopping a running container did not open a confirm: %q",
				a.status)
		}
		checkFrame(t, a.View(), width, "confirm")

		a.mode = modeBrowse
		press(a, "o")
		if a.mode != modePicker {
			t.Fatalf("the policy key did not open a picker: %q", a.status)
		}
		checkFrame(t, a.View(), width, "picker")

		a.mode = modeBrowse
		press(a, "/")
		if a.mode != modeFilter {
			t.Fatal("the filter key did not open the prompt")
		}
		checkFrame(t, a.View(), width, "filter")
	}
}

// checkFrame asserts that every line of a rendered frame fits.
func checkFrame(t *testing.T, frame string, width int, what string) {
	t.Helper()
	for number, line := range strings.Split(frame, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("%s at width %d: line %d is %d cells wide",
				what, width, number+1, got)
		}
	}
}

// TestTabsReachEveryScreen: the tab bar advertises four, and all four have to
// answer.
func TestTabsReachEveryScreen(t *testing.T) {
	a, _ := newTestApp(t)
	for i := screen(0); i < screenCount; i++ {
		if a.screen != i {
			t.Errorf("after %d tabs the screen is %v", i, a.screen)
		}
		if a.rowCount() == 0 {
			t.Errorf("%s has no rows on the sample machine", a.screen.title())
		}
		press(a, "tab")
	}
	if a.screen != screenContainers {
		t.Errorf("the tabs do not wrap: ended on %v", a.screen)
	}
}

// TestFilterAppliesToEveryScreen: one filter, applied to whichever screen is
// in front, is what the prompt promises.
func TestFilterAppliesToEveryScreen(t *testing.T) {
	a, _ := newTestApp(t)
	a.filter = "shopfront"
	a.applyFilter()
	if len(a.containers) != 3 {
		t.Errorf("%d containers match the project, want 3", len(a.containers))
	}
	for _, c := range a.containers {
		if !strings.Contains(strings.ToLower(c.Haystack()), "shopfront") {
			t.Errorf("%s does not match the filter", c.Name)
		}
	}

	a.filter = "podman"
	a.applyFilter()
	for _, c := range a.containers {
		if c.Target.Engine != container.EnginePodman {
			t.Errorf("%s is not a podman container but matched \"podman\"", c.Name)
		}
	}

	a.filter = "nothing-here-at-all"
	a.applyFilter()
	if a.rowCount() != 0 {
		t.Errorf("a filter matching nothing left %d rows", a.rowCount())
	}
	// The empty state has to say the filter is why, rather than claim the
	// machine is empty.
	if !strings.Contains(a.View(), "nothing matches") {
		t.Error("the empty state does not blame the filter")
	}
}

// TestAKeyProducesExactlyThePreviewedCommand is the family's promise, checked
// through the key map: pressing x opens a dialog whose command is the one that
// runs, and nothing runs before the answer.
func TestAKeyProducesExactlyThePreviewedCommand(t *testing.T) {
	a, backend := newTestApp(t)
	c := selectByName(t, a, "shopfront-api")

	press(a, "x")
	if a.mode != modeConfirm {
		t.Fatalf("x did not open a confirm: %q", a.status)
	}
	action, ok := a.confirm.Payload.(container.Action)
	if !ok {
		t.Fatal("the confirm carries no action")
	}
	if len(action.Commands) != 1 {
		t.Fatalf("stopping a container built %d commands", len(action.Commands))
	}
	if got := action.Commands[0].String(); got != "docker stop "+c.ID {
		t.Errorf("command = %q", got)
	}
	if a.confirm.Command != backend.Preview(action.Target, action.Commands[0]) {
		t.Errorf("the dialog shows %q, the runner would run %q",
			a.confirm.Command, backend.Preview(action.Target, action.Commands[0]))
	}
	// Nothing has run: the dialog is a question.
	if ran := backend.Ran(); len(ran) != 0 {
		t.Errorf("opening the dialog ran %v", ran)
	}

	// Answering no runs nothing either.
	press(a, "n")
	if ran := backend.Ran(); len(ran) != 0 {
		t.Errorf("cancelling ran %v", ran)
	}
	if a.mode != modeBrowse {
		t.Errorf("cancelling left the app in mode %v", a.mode)
	}
}

// TestRemovingARunningContainerAsksFirst: `rm -f` kills before it removes, and
// the choice between stopping first and killing is the user's.
func TestRemovingARunningContainerAsksFirst(t *testing.T) {
	a, _ := newTestApp(t)
	selectByName(t, a, "shopfront-api")

	press(a, "d")
	if a.mode != modePicker {
		t.Fatalf("removing a running container went straight to a confirm: %q",
			a.status)
	}
	// The second option is the forced one, and choosing it must produce a
	// command that says so.
	a.picker.Cursor = 1
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.mode != modeConfirm {
		t.Fatalf("the picker did not lead to a confirm: %q", a.status)
	}
	action := a.confirm.Payload.(container.Action)
	if !strings.Contains(action.Commands[0].String(), "rm -f") {
		t.Errorf("the forced removal is %q", action.Commands[0])
	}
	if action.Warning == "" {
		t.Error("a forced removal carries no warning")
	}
	if !a.confirm.Danger {
		t.Error("a forced removal is not painted as dangerous")
	}
}

// TestRemovingAStoppedContainerGoesStraightToTheConfirm: there is nothing to
// choose, so there is no picker.
func TestRemovingAStoppedContainerGoesStraightToTheConfirm(t *testing.T) {
	a, _ := newTestApp(t)
	c := selectByName(t, a, "nightly-backup")
	press(a, "d")
	if a.mode != modeConfirm {
		t.Fatalf("removing a stopped container did not confirm: %q", a.status)
	}
	action := a.confirm.Payload.(container.Action)
	if got := action.Commands[0].String(); got != "docker rm "+c.ID {
		t.Errorf("command = %q", got)
	}
}

// TestAVerbThatDoesNotApplyExplainsItself: a key that silently did nothing
// would be the worst outcome, so the refusal reaches the status line.
func TestAVerbThatDoesNotApplyExplainsItself(t *testing.T) {
	a, _ := newTestApp(t)
	selectByName(t, a, "shopfront-api")
	press(a, "s")
	if a.mode == modeConfirm {
		t.Fatal("a running container was offered a start")
	}
	if !strings.Contains(a.status, "already running") {
		t.Errorf("status = %q, want it to say why", a.status)
	}
	if a.statusKind != ui.StatusWarn {
		t.Errorf("the refusal is not shown as a warning")
	}
}

// TestComposeNeedsTheLabels: a container with no project cannot be driven
// through Compose, and the message says which label is missing.
func TestComposeNeedsTheLabels(t *testing.T) {
	a, _ := newTestApp(t)
	selectByName(t, a, "nightly-backup")
	press(a, "c")
	if a.mode == modePicker {
		t.Fatal("a container with no compose labels opened the project picker")
	}
	if !strings.Contains(a.status, "compose") {
		t.Errorf("status = %q", a.status)
	}

	selectByName(t, a, "shopfront-api")
	press(a, "c")
	if a.mode != modePicker {
		t.Fatalf("a compose member did not open the picker: %q", a.status)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.mode != modeConfirm {
		t.Fatalf("the compose picker did not lead to a confirm: %q", a.status)
	}
	line := a.confirm.Payload.(container.Action).Commands[0].String()
	if !strings.Contains(line, "compose --project-name shopfront") {
		t.Errorf("the compose command is %q", line)
	}
}

// TestPruneIsAlwaysAChoice: every variant is a sentence a reader picks, and the
// difference between them is what gets deleted.
func TestPruneIsAlwaysAChoice(t *testing.T) {
	a, _ := newTestApp(t)
	a.screen = screenImages
	a.clampCursor()

	press(a, "X")
	if a.mode != modePicker {
		t.Fatalf("X did not open the prune picker: %q", a.status)
	}
	if len(a.picker.Options) != len(pruneOptions) {
		t.Errorf("the picker offers %d prunes, the tool builds %d",
			len(a.picker.Options), len(pruneOptions))
	}
	// Every option has to build something, or it is a dead end in the list.
	for i := range pruneOptions {
		a.mode, a.pickerKind = modePicker, pickerPrune
		a.pendingT = a.images[0].Target
		a.picker = ui.NewPicker("", pruneOptions, "")
		a.picker.Cursor = i
		a.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if a.mode != modeConfirm {
			t.Fatalf("%q built nothing: %q", pruneOptions[i], a.status)
		}
		action := a.confirm.Payload.(container.Action)
		if !action.Destructive {
			t.Errorf("%q is not marked destructive", pruneOptions[i])
		}
		a.confirm = ui.Confirm{}
	}
}

// TestTheLogPaneStartsNoProcessItDoesNotWaitFor: the pane re-reads on a timer,
// so the command it builds must never be a follow.
func TestTheLogPaneStartsNoProcessItDoesNotWaitFor(t *testing.T) {
	a, backend := newTestApp(t)
	c := selectByName(t, a, "shopfront-api")
	press(a, "L")
	if a.mode != modeLogs {
		t.Fatalf("L did not open the log pane: %q", a.status)
	}
	line := backend.LogsCommand(c, a.logsOpts)
	for _, forbidden := range []string{" -f", "--follow"} {
		if strings.Contains(line, forbidden) {
			t.Errorf("the log command follows: %q", line)
		}
	}
	// The pane names the command it is reading, for the same reason every
	// dialog does.
	if !strings.Contains(a.View(), "logs --tail") {
		t.Error("the pane does not show the command it read")
	}

	// The pane's own keys change the read rather than acting on the container.
	before := a.logsOpts.Tail
	press(a, "n")
	if a.logsOpts.Tail == before {
		t.Error("n did not change the window")
	}
	press(a, "t")
	if a.logsOpts.Timestamps {
		t.Error("t did not toggle timestamps off")
	}
	// d in the pane must not remove the container behind it.
	press(a, "d")
	if a.mode != modeLogs {
		t.Errorf("a stray d in the log pane left the pane: mode %v", a.mode)
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Errorf("the log pane ran %v", ran)
	}
}

// TestDetailShowsTheContainerItWasOpenedFrom, and its action keys act on that
// container rather than on whatever the list behind it has moved to.
func TestDetailShowsTheContainerItWasOpenedFrom(t *testing.T) {
	a, _ := newTestApp(t)
	c := selectByName(t, a, "shopfront-api")
	press(a, "enter")
	if a.mode != modeDetail {
		t.Fatalf("enter did not open the detail: %q", a.status)
	}
	if a.detailFor != c.ID {
		t.Errorf("the detail is for %q, want %q", a.detailFor, c.ID)
	}

	// The read arrives.
	detail, err := a.backend.Inspect(t.Context(), c)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	a.Update(detailMsg{id: c.ID, detail: detail})

	// The whole detail is asserted on rather than the visible window: the
	// screen scrolls, and the sections below the fold are still what it shows.
	frame := strings.Join(a.detailLines(), "\n")
	if !strings.Contains(a.View(), c.Name) {
		t.Error("the rendered frame does not name the container")
	}
	for _, want := range []string{"unhealthy", "Environment", "Mounts",
		"Health check"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the detail screen does not show %q", want)
		}
	}
	// The secret values never reach the screen; the names do.
	for _, secret := range []string{"hunter2", "sk_live", "a-long-string"} {
		if strings.Contains(frame, secret) {
			t.Errorf("the detail screen printed %q", secret)
		}
	}
	if !strings.Contains(frame, "DATABASE_PASSWORD") {
		t.Error("the detail screen hides the name as well as the value")
	}

	// An answer for a container the reader has already left is discarded.
	a.Update(detailMsg{id: "not-this-one", detail: container.Detail{}})
	if a.detail.Container.ID != c.ID {
		t.Error("a stale inspect overwrote the open detail")
	}
}

// TestTheHeaderCountsWhatIsWrong: it is the first thing on screen because it is
// why anyone opened the tool.
func TestTheHeaderCountsWhatIsWrong(t *testing.T) {
	a, _ := newTestApp(t)
	frame := a.headerView()
	for _, want := range []string{"containers", "running", "need attention",
		"dangling images"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the header does not report %q", want)
		}
	}
	// And it names every engine scope, including the one that was not read.
	for _, want := range []string{"docker", "podman", "podman (system)"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the header does not name %q", want)
		}
	}
}

// TestTheEnginesScreenSaysWhyOneWasNotRead: an empty screen nobody can
// interpret is the failure this line prevents.
func TestTheEnginesScreenSaysWhyOneWasNotRead(t *testing.T) {
	a, _ := newTestApp(t)
	a.screen = screenSystem
	a.clampCursor()
	frame := a.View()
	if !strings.Contains(frame, "not read") {
		t.Error("the engines screen does not report the scope that was not read")
	}
	if !strings.Contains(frame, "quadlet") {
		t.Error("the engines screen does not list the Quadlet units")
	}
}

// TestHelpListsEveryActionKey: the help screen and the key map are two views of
// one table, and a key missing from the help is a key nobody will find.
func TestHelpListsEveryActionKey(t *testing.T) {
	var listed string
	for _, hint := range helpKeys() {
		listed += hint.Key + " "
	}
	for _, key := range []string{"L", "s / x", "r", "K", "p / u", "d", "o",
		"U", "c", "X", "A", "/", "?", "q"} {
		if !strings.Contains(listed, key) {
			t.Errorf("the help screen does not list %q", key)
		}
	}
}
