package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// The fields the two forms are made of, named rather than numbered so the
// picker knows which one it is filling.
const (
	fieldImage   = "image"
	fieldName    = "name"
	fieldPorts   = "ports"
	fieldVolumes = "volumes"
	fieldEnvFile = "envfile"
	fieldRestart = "restart"
	fieldWhat    = "what"
	fieldDriver  = "driver"
)

// formKind is what an open form is building, so the submit knows which builder
// to call.
type formKind int

const (
	// formRun is the new-container form. It is the zero value because a form
	// that was never opened is never submitted: the mode decides that, and the
	// kind only has to tell the two forms apart.
	formRun formKind = iota
	// formStorage is the one that makes a volume or a network, which are the
	// same three questions with a different noun.
	formStorage
)

// The two values the storage form's first field takes.
const (
	whatVolume  = "volume"
	whatNetwork = "network"
)

// formField is one row of a form.
type formField struct {
	key   string
	label string
	// options is the closed set of values, nil for a free-text field.
	options []string
	help    string
}

// choice reports whether the field is one the picker serves.
func (f formField) choice() bool { return len(f.options) > 0 }

// form is a guided creation: a list of fields, the values collected so far,
// and one text box that follows the cursor.
//
// It is the same shape for both things this tool creates, because both are the
// same act: answer a few questions, see the exact command line they produced,
// and confirm it. Nothing here builds an argv — what a name, a port mapping or
// a mount may be is the engine package's rule, checked once where the argv is
// built.
type form struct {
	kind   formKind
	target container.Target
	fields []formField
	values map[string]string
	active int
	input  textinput.Model
	// drivers are the two closed sets the storage form's driver field switches
	// between, kept here because the field's options change with the noun.
	volumeDrivers  []string
	networkDrivers []string
}

// newRunForm builds the new-container form, seeded from the image of the row
// the reader was on: starting a second copy of something already here is the
// commonest reason to open it.
func newRunForm(target container.Target, image string,
	policies []string) form {
	if len(policies) == 0 {
		policies = []string{"no", "always", "unless-stopped", "on-failure"}
	}
	f := form{
		kind:   formRun,
		target: target,
		values: map[string]string{
			fieldImage:   strings.TrimSpace(image),
			fieldName:    "",
			fieldPorts:   "",
			fieldVolumes: "",
			fieldEnvFile: "",
			fieldRestart: policies[0],
		},
		fields: []formField{
			{key: fieldImage, label: "Image",
				help: "The reference to run, as the engine spells it: " +
					"nginx:1.27, ghcr.io/owner/app:2.4.1. No tag is read as :latest."},
			{key: fieldName, label: "Name",
				help: "What the container is called. Leave it empty and the engine " +
					"invents one, which is a name nobody will recognise later."},
			{key: fieldPorts, label: "Ports",
				help: "host:container, space or comma separated: 8080:80, " +
					"5353:53/udp. Empty publishes nothing."},
			{key: fieldVolumes, label: "Volumes",
				help: "source:destination[:ro], space or comma separated. The " +
					"source is an absolute path or the name of a volume."},
			{key: fieldEnvFile, label: "Env file",
				help: "Absolute path to a file of NAME=value lines, read by the " +
					"engine. There is no field for a value: a secret typed here " +
					"would be on screen and in inspect afterwards."},
			{key: fieldRestart, label: "Restart", options: policies,
				help: "What the engine does when it exits, and at boot. " +
					"unless-stopped is what a service wants; no is what a one-shot " +
					"job wants."},
		},
	}
	f.newInput()
	return f
}

// newStorageForm builds the volume-or-network form, opened on the noun of the
// row the reader was on.
func newStorageForm(target container.Target, what string,
	volumeDrivers, networkDrivers []string) form {
	if what != whatNetwork {
		what = whatVolume
	}
	f := form{
		kind:           formStorage,
		target:         target,
		volumeDrivers:  volumeDrivers,
		networkDrivers: networkDrivers,
		values:         map[string]string{fieldWhat: what, fieldName: ""},
	}
	f.fields = []formField{
		{key: fieldWhat, label: "What",
			options: []string{whatVolume, whatNetwork},
			help: "A volume is where a container keeps what survives being " +
				"removed. A network is how containers reach each other by name."},
		{key: fieldName, label: "Name",
			help: "What it is called. It starts with a letter or a digit and " +
				"carries only letters, digits, and _ . -"},
		{key: fieldDriver, label: "Driver", options: f.driversFor(what),
			help: "The driver the engine implements it with. local and bridge are " +
				"the ones every machine has."},
	}
	f.values[fieldDriver] = f.driversFor(what)[0]
	f.newInput()
	return f
}

// driversFor is the closed driver set for a noun, with a fallback so a backend
// that declares none still gives the picker something to show.
func (f form) driversFor(what string) []string {
	list := f.volumeDrivers
	fallback := "local"
	if what == whatNetwork {
		list, fallback = f.networkDrivers, "bridge"
	}
	if len(list) == 0 {
		return []string{fallback}
	}
	return list
}

// newInput builds the text box and focuses the first field.
func (f *form) newInput() {
	f.input = textinput.New()
	f.input.CharLimit = 300
	f.input.Prompt = ""
	f.focusActive()
}

// title names the form for its dialog.
func (f form) title() string {
	if f.kind == formStorage {
		if f.values[fieldWhat] == whatNetwork {
			return "Create a network on " + f.target.String()
		}
		return "Create a volume on " + f.target.String()
	}
	return "Run a new container on " + f.target.String()
}

// current is the field being edited.
func (f form) current() formField {
	if f.active < 0 || f.active >= len(f.fields) {
		return formField{}
	}
	return f.fields[f.active]
}

// focusActive loads the active field into the text box, or blurs it for a
// choice field.
func (f *form) focusActive() {
	field := f.current()
	if field.choice() || field.key == "" {
		f.input.Blur()
		return
	}
	f.input.SetValue(f.values[field.key])
	f.input.Focus()
	f.input.CursorEnd()
}

// save writes the text box back into the values before the field changes.
func (f *form) save() {
	field := f.current()
	if field.key != "" && !field.choice() {
		f.values[field.key] = f.input.Value()
	}
}

// next moves to the following field.
func (f *form) next() {
	f.save()
	f.active = (f.active + 1) % len(f.fields)
	f.focusActive()
}

// prev moves to the previous field.
func (f *form) prev() {
	f.save()
	count := len(f.fields)
	f.active = (f.active + count - 1) % count
	f.focusActive()
}

// activeIsChoice reports whether the active field is one the picker serves.
func (f form) activeIsChoice() bool { return f.current().choice() }

// activeKey, activeLabel, activeOptions and activeValue expose the active
// field to the picker dialog.
func (f form) activeKey() string       { return f.current().key }
func (f form) activeLabel() string     { return f.current().label }
func (f form) activeOptions() []string { return f.current().options }
func (f form) activeValue() string     { return f.values[f.current().key] }

// set applies a value chosen in the picker to a field.
//
// Changing the storage form's noun changes what a driver may be, so the driver
// field is rebuilt rather than left holding a value the other noun's engine
// would refuse.
func (f *form) set(field, value string) {
	if field == "" {
		return
	}
	f.values[field] = value
	if field == fieldWhat {
		drivers := f.driversFor(value)
		for i := range f.fields {
			if f.fields[i].key == fieldDriver {
				f.fields[i].options = drivers
			}
		}
		f.values[fieldDriver] = drivers[0]
	}
	f.focusActive()
}

// cycle moves a choice field one step.
func (f *form) cycle(delta int) {
	field := f.current()
	if !field.choice() {
		return
	}
	index := 0
	for i, option := range field.options {
		if option == f.values[field.key] {
			index = i
		}
	}
	index = (index + delta + len(field.options)) % len(field.options)
	f.set(field.key, field.options[index])
}

// updateActive forwards a message to the value field when it is a text box.
func (f *form) updateActive(msg tea.Msg) tea.Cmd {
	if f.current().choice() {
		return nil
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

// runSpec is what the run form collected. Only the splitting lives here: what
// each entry may be is checked in the engine package, where the argv is built.
func (f *form) runSpec() container.RunSpec {
	f.save()
	return container.RunSpec{
		Target:        f.target,
		Image:         strings.TrimSpace(f.values[fieldImage]),
		Name:          strings.TrimSpace(f.values[fieldName]),
		Ports:         splitList(f.values[fieldPorts]),
		Volumes:       splitList(f.values[fieldVolumes]),
		EnvFile:       strings.TrimSpace(f.values[fieldEnvFile]),
		RestartPolicy: strings.TrimSpace(f.values[fieldRestart]),
	}
}

// volumeSpec and networkSpec are what the storage form collected.
func (f *form) volumeSpec() container.VolumeSpec {
	f.save()
	return container.VolumeSpec{
		Target: f.target,
		Name:   strings.TrimSpace(f.values[fieldName]),
		Driver: strings.TrimSpace(f.values[fieldDriver]),
	}
}

func (f *form) networkSpec() container.NetworkSpec {
	f.save()
	return container.NetworkSpec{
		Target: f.target,
		Name:   strings.TrimSpace(f.values[fieldName]),
		Driver: strings.TrimSpace(f.values[fieldDriver]),
	}
}

// makesNetwork reports which of the two nouns the storage form is on.
func (f form) makesNetwork() bool { return f.values[fieldWhat] == whatNetwork }

// splitList splits a list field. Both separators are accepted because both are
// what somebody types.
func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})
}

// view renders the form as a dialog.
func (f form) view(t theme.Theme, width, height int) string {
	inner := min(max(width-8, 34), 76)
	labelWidth := min(12, max(inner-16, 8))
	valueWidth := max(inner-labelWidth-6, 10)

	lines := []string{t.Title.Render(ui.Truncate(f.title(), inner-4)), ""}

	for i, field := range f.fields {
		label := t.Muted.Render(ui.Pad(ui.Truncate(field.label, labelWidth),
			labelWidth))
		var value string
		switch {
		case field.choice():
			value = renderChoice(t, f.values[field.key], i == f.active, valueWidth)
		case i == f.active:
			input := f.input
			input.Width = valueWidth - 2
			value = input.View()
		default:
			value = t.Base.Render(ui.Truncate(orPlaceholder(f.values[field.key]),
				valueWidth))
		}
		marker := "  "
		if i == f.active {
			marker = t.Accent.Render("> ")
		}
		lines = append(lines, marker+label+"  "+value)
	}

	if help := f.current().help; help != "" {
		lines = append(lines, "", t.Muted.Render(help))
	}
	lines = append(lines, "",
		t.Muted.Render(ui.Truncate(f.footnote(), inner-4)),
		"",
		t.Key.Render("tab")+t.KeyDesc.Render(" next  ")+
			t.Key.Render("←/→")+t.KeyDesc.Render(" change  ")+
			t.Key.Render("space")+t.KeyDesc.Render(" list  ")+
			t.Key.Render("enter")+t.KeyDesc.Render(" review  ")+
			t.Key.Render("esc")+t.KeyDesc.Render(" cancel"))

	box := t.Dialog.Width(inner).Render(strings.Join(lines, "\n"))
	return placeCenter(box, width, height)
}

// footnote is the one line under every form: what it will not do.
func (f form) footnote() string {
	if f.kind == formStorage {
		return "Nothing is attached to it until a container uses it."
	}
	return "Detached, and owned by nothing: compose will not adopt it."
}

// orPlaceholder renders an empty value as something visible, so a blank row is
// never mistaken for a broken one.
func orPlaceholder(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// renderChoice draws a choice field with its cycling arrows.
func renderChoice(t theme.Theme, value string, active bool, width int) string {
	value = ui.Truncate(orPlaceholder(value), width-4)
	if active {
		return t.Accent.Render("‹ ") + t.Base.Render(value) + t.Accent.Render(" ›")
	}
	return t.Base.Render("  " + value)
}
