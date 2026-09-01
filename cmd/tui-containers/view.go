package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-containers/internal/container"
	"github.com/tui-tools/tui-kit/ui"
)

// Layout constants: the rows the table cannot use.
const (
	headerLines = 2
	footerLines = 2
	// tabLines is the one row the tab bar takes.
	tabLines = 1
	// minTableHeight keeps at least one visible row on a very short terminal.
	minTableHeight = 1
)

// tableHeight is the number of rows that fit on screen.
func (a *app) tableHeight() int {
	// header + tabs + table header + footer + status line.
	return max(a.height-headerLines-tabLines-footerLines-2, minTableHeight)
}

// detailHeight is the number of detail lines that fit on screen.
func (a *app) detailHeight() int {
	return max(a.height-headerLines-tabLines-footerLines-1, minTableHeight)
}

// logsHeight is the number of log lines that fit on screen.
func (a *app) logsHeight() int { return a.detailHeight() }

// logsBottom is the offset that shows the end of the log, which is where a
// reader following one wants to be.
func (a *app) logsBottom() int {
	return max(len(a.logLines())-a.logsHeight(), 0)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeFilter, modePrompt:
		return a.input.View(a.theme, a.width, a.height)
	case modePicker:
		return a.picker.View(a.theme, a.width, a.height)
	case modeForm:
		return a.form.view(a.theme, a.width, a.height)
	case modeHelp:
		return placeCenter(
			ui.HelpScreen(a.theme, "tui-containers — keys", helpKeys(), a.width),
			a.width, a.height)
	case modeDetail:
		return a.detailView()
	case modeLogs:
		return a.logsView()
	}
	return a.browseView()
}

// placeCenter centers a rendered box in the terminal.
func placeCenter(box string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// browseView renders a screen: header, tab bar, table, help bar, status.
func (a *app) browseView() string {
	header := a.headerView()
	tabs := a.tabsView()

	var body string
	switch {
	case a.loading && a.rowCount() == 0:
		body = ui.EmptyState(a.theme, "reading the engines…", a.width,
			a.tableHeight()+1)
	case a.rowCount() == 0 && a.filter != "":
		body = ui.EmptyState(a.theme, "nothing matches "+strconv.Quote(a.filter),
			a.width, a.tableHeight()+1)
	case a.rowCount() == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme,
			"could not read any engine — see the message below",
			a.width, a.tableHeight()+1)
	case a.rowCount() == 0:
		body = ui.EmptyState(a.theme, a.emptyMessage(), a.width, a.tableHeight()+1)
	default:
		body = a.table()
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(),
		a.width)
	return strings.Join([]string{header, tabs, body, help, status}, "\n")
}

// emptyMessage is what a screen with no rows says, which is different on each.
func (a *app) emptyMessage() string {
	if len(a.model.Available()) == 0 {
		return "no container engine answered on this machine"
	}
	switch a.screen {
	case screenImages:
		return "no images: every engine here has an empty store"
	case screenStorage:
		return "no volumes and no networks beyond the ones the engines made"
	case screenSystem:
		return "no engine reported anything about itself"
	default:
		return "no containers at all, running or stopped"
	}
}

// headerView renders the facts at the top of every screen.
func (a *app) headerView() string {
	t := a.theme

	running := len(a.model.Running())
	facts := []ui.Fact{
		{Label: "containers", Value: strconv.Itoa(len(a.model.Containers))},
		{Label: "running", Value: strconv.Itoa(running)},
	}

	// What is wrong comes first among the coloured facts, because it is why
	// anyone opened the tool.
	if count := len(a.model.Attention()); count > 0 {
		style := t.Danger
		facts = append(facts, ui.Fact{Label: "need attention",
			Value: strconv.Itoa(count), Style: &style})
	}
	if count := len(a.model.Dangling()); count > 0 {
		style := t.Warn
		facts = append(facts, ui.Fact{Label: "dangling images",
			Value: strconv.Itoa(count), Style: &style})
	}

	// Which engines answered, and which are here and did not. An engine that
	// is installed and silent is the fact that explains an empty screen.
	for _, info := range a.model.Engines {
		style := t.OK
		value := "answering"
		switch {
		case !info.Available:
			style, value = t.Muted, "not read"
		case info.Rootless:
			value = "rootless"
		}
		facts = append(facts, ui.Fact{Label: info.Target.String(),
			Value: value, Style: &style})
	}

	// The engine versions, when they were probed: quiet on a tested version,
	// coloured on one nobody has run against.
	for _, result := range probed(a.backendCompat) {
		facts = append(facts, ui.CompatFact(t, result))
	}

	subtitle := a.backend.Describe()
	if a.filter != "" {
		subtitle += "  ·  filter: " + a.filter
	}
	return ui.Header{Title: "tui-containers", Subtitle: subtitle, Facts: facts}.
		Render(t, a.width)
}

// tabsView renders the four screens as one row, with the current one accented.
func (a *app) tabsView() string {
	var parts []string
	for s := screen(0); s < screenCount; s++ {
		label := strconv.Itoa(int(s)+1) + " " + s.title()
		if s == a.screen {
			parts = append(parts, a.theme.Accent.Render("["+label+"]"))
			continue
		}
		parts = append(parts, a.theme.Muted.Render(" "+label+" "))
	}
	return a.theme.Footer.Width(a.width).Render(
		ui.Truncate(strings.Join(parts, " "), a.width-2))
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	count := strconv.Itoa(a.rowCount())
	suffix := "  ·  tab to move  ·  ? for help"
	switch a.screen {
	case screenImages:
		return count + " images  ·  d removes one, X prunes" + suffix
	case screenStorage:
		return count + " volumes and networks  ·  d removes one" + suffix
	case screenSystem:
		return "what each engine says about itself" + suffix
	default:
		return count + " containers  ·  enter opens one, L reads its log" + suffix
	}
}

// table renders the current screen's rows.
func (a *app) table() string {
	columns, rows, styles := a.tableData()
	return ui.Table{
		Columns:  columns,
		Rows:     rows,
		Styles:   styles,
		Selected: a.cursor[a.screen],
		Offset:   a.offset[a.screen],
		Height:   a.tableHeight(),
	}.Render(a.theme, a.width)
}

// tableData builds the columns, cells and row styles of the current screen.
// Every screen drops its widest columns first on a narrow terminal, which is
// what keeps a 40-column pane readable.
func (a *app) tableData() ([]ui.Column, [][]string, []*lipgloss.Style) {
	switch a.screen {
	case screenImages:
		return a.imagesTable()
	case screenStorage:
		return a.storageTable()
	case screenSystem:
		return a.systemTable()
	default:
		return a.containersTable()
	}
}

// containersTable is the merged list: what it is called, what it is running,
// what state it is in, how long it has been that way, and where it is reachable.
func (a *app) containersTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "CONTAINER", Width: 22, Flex: true},
		{Title: "STATE", Width: 12},
	}
	// The columns are added in the order they earn their space on a merged
	// list. The image comes first because it is what the container is; the
	// engine next, because on a machine with two of them a name alone does not
	// say which store a row is in; the uptime after that; and the ports last,
	// because they are on the detail screen and the others are not.
	showImage := a.width >= 72
	showEngine := a.width >= 88
	showUptime := a.width >= 100
	showPorts := a.width >= 118
	if showImage {
		columns = append(columns, ui.Column{Title: "IMAGE", Width: 20, Flex: true})
	}
	if showEngine {
		columns = append(columns, ui.Column{Title: "ENGINE", Width: 15})
	}
	if showUptime {
		columns = append(columns, ui.Column{Title: "UPTIME", Width: 10})
	}
	if showPorts {
		columns = append(columns, ui.Column{Title: "PORTS", Width: 18, Flex: true})
	}

	rows := make([][]string, 0, len(a.containers))
	styles := make([]*lipgloss.Style, 0, len(a.containers))
	for _, c := range a.containers {
		row := []string{containerName(c), stateCell(c)}
		if showImage {
			row = append(row, c.Image)
		}
		if showEngine {
			row = append(row, c.Target.String())
		}
		if showUptime {
			row = append(row, orNone(c.Uptime))
		}
		if showPorts {
			row = append(row, orNone(c.PortsText()))
		}
		rows = append(rows, row)
		styles = append(styles, a.containerStyle(c))
	}
	return columns, rows, styles
}

// containerName prefixes a Compose member with its project, so three rows of
// one project read as one thing rather than as three unrelated containers.
func containerName(c container.Container) string {
	if c.Project == "" {
		return c.Name
	}
	service := c.Service
	if service == "" {
		service = c.Name
	}
	return c.Project + "/" + service
}

// stateCell is the state column: the engine's word, with the health verdict
// after it when the container has a check and the check disagrees.
func stateCell(c container.Container) string {
	state := string(c.State)
	if state == "" {
		state = "?"
	}
	switch {
	case c.Unhealthy():
		return state + " !"
	case c.State == container.StateExited && c.ExitCodeKnown && c.ExitCode != 0:
		return state + " " + strconv.Itoa(c.ExitCode)
	case c.Health == container.HealthStarting:
		return state + " ~"
	default:
		return state
	}
}

// imagesTable is the store: what each image is, what it costs, and whether
// anything still points at it.
func (a *app) imagesTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "IMAGE", Width: 30, Flex: true},
		{Title: "SIZE", Width: 10},
	}
	showUsed := a.width >= 66
	showID := a.width >= 84
	showEngine := a.width >= 104
	if showUsed {
		columns = append(columns, ui.Column{Title: "USED BY", Width: 8})
	}
	if showID {
		columns = append(columns, ui.Column{Title: "ID", Width: 14})
	}
	if showEngine {
		columns = append(columns, ui.Column{Title: "ENGINE", Width: 16})
	}

	rows := make([][]string, 0, len(a.images))
	styles := make([]*lipgloss.Style, 0, len(a.images))
	for _, image := range a.images {
		name := image.Name()
		if image.Dangling {
			name = "<dangling> " + image.ID
		}
		row := []string{name, orNone(image.SizeText)}
		if showUsed {
			row = append(row, usedCell(image))
		}
		if showID {
			row = append(row, image.ID)
		}
		if showEngine {
			row = append(row, image.Target.String())
		}
		rows = append(rows, row)

		style := a.theme.Row
		switch {
		case image.Dangling:
			style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
		case image.UsedBy == 0:
			style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
		}
		styles = append(styles, &style)
	}
	return columns, rows, styles
}

// usedCell renders how many containers were created from an image.
func usedCell(image container.Image) string {
	if image.UsedBy == 0 {
		return "—"
	}
	return strconv.Itoa(image.UsedBy)
}

// storageTable is the volumes and the networks on one screen: they are the two
// things a container leaves behind, and both are answered by the same question
// — is anything still using this.
func (a *app) storageTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "KIND", Width: 8},
		{Title: "NAME", Width: 28, Flex: true},
		{Title: "IN USE", Width: 7},
	}
	showDriver := a.width >= 70
	showWhere := a.width >= 96
	showEngine := a.width >= 116
	if showDriver {
		columns = append(columns, ui.Column{Title: "DRIVER", Width: 10})
	}
	if showWhere {
		columns = append(columns, ui.Column{Title: "WHERE", Width: 30, Flex: true})
	}
	if showEngine {
		columns = append(columns, ui.Column{Title: "ENGINE", Width: 16})
	}

	rows := make([][]string, 0, len(a.storage))
	styles := make([]*lipgloss.Style, 0, len(a.storage))
	for _, entry := range a.storage {
		kind, name, inUse, driver, where, target := storageCells(entry)
		row := []string{kind, name, yesNo(inUse)}
		if showDriver {
			row = append(row, orNone(driver))
		}
		if showWhere {
			row = append(row, orNone(where))
		}
		if showEngine {
			row = append(row, target.String())
		}
		rows = append(rows, row)

		style := a.theme.Row
		if !inUse {
			style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
		}
		styles = append(styles, &style)
	}
	return columns, rows, styles
}

// storageCells flattens a volume or a network into the same six values.
func storageCells(entry storageRow) (kind, name string, inUse bool,
	driver, where string, target container.Target) {
	if entry.volume != nil {
		v := entry.volume
		name = v.Name
		if v.Anonymous {
			name = "(anonymous) " + shortID(v.Name)
		}
		return "volume", name, v.InUse, v.Driver, v.Mountpoint, v.Target
	}
	n := entry.network
	name = n.Name
	if n.Builtin {
		name += "  (built in)"
	}
	where = ""
	if n.Internal {
		where = "internal: no route off this machine"
	}
	return "network", name, n.InUse, n.Driver, where, n.Target
}

// shortID truncates an engine-generated name to something a column can hold.
func shortID(name string) string {
	if len(name) > 12 {
		return name[:12]
	}
	return name
}

// buildSystemRows flattens what each engine says about itself, so the screen
// scrolls and filters like the other three.
func (a *app) buildSystemRows(keep func(string) bool) []systemRow {
	var rows []systemRow
	add := func(target container.Target, label, value string, warn bool) {
		if keep(label + " " + value + " " + target.String()) {
			rows = append(rows, systemRow{label: label, value: value,
				target: target, warn: warn})
		}
	}

	for _, info := range a.model.Engines {
		target := info.Target
		if !info.Available {
			add(target, target.String(), "not read: "+orNone(info.Detail), true)
			continue
		}
		add(target, target.String(), engineSummary(info), false)
		add(target, "  version", orNone(info.ServerVersion), false)
		add(target, "  storage", orNone(info.StorageDriver)+" at "+
			orNone(info.Root), false)
		add(target, "  cgroup", "v"+orNone(info.CgroupVersion)+
			", rootless "+yesNo(info.Rootless), false)
		add(target, "  compose", composeCell(info), false)
		if len(info.RegistryMirrors) > 0 {
			add(target, "  mirrors", strings.Join(info.RegistryMirrors, ", "), false)
		}
		if len(info.SearchRegistries) > 0 {
			add(target, "  search", strings.Join(info.SearchRegistries, ", "), false)
		}
		for _, row := range info.Disk {
			add(target, "  "+strings.ToLower(row.Type),
				row.Size+" total, "+row.Reclaimable+" reclaimable, "+
					row.Active+" of "+row.Total+" in use", false)
		}
		for _, quadlet := range info.Quadlets {
			add(target, "  quadlet", quadlet.Path+" → "+quadlet.Name, false)
		}
		if len(info.Quadlets) > 0 {
			add(target, "  quadlet", "read only in v0.1: changing one means "+
				"regenerating and restarting its unit", false)
		}
		if info.Detail != "" {
			add(target, "  note", info.Detail, true)
		}
	}

	for _, c := range a.model.Attention() {
		add(c.Target, "attention", c.Name+": "+orNone(c.Status), true)
	}
	for _, result := range a.backendCompat {
		add(container.Target{Engine: container.Engine(result.Backend)},
			"version", result.Backend+" "+orNone(result.Version)+" ("+
				result.Status.String()+")", false)
	}
	return rows
}

// engineSummary is the one-line answer for an engine that is here and working.
func engineSummary(info container.EngineInfo) string {
	parts := []string{
		strconv.Itoa(info.Running) + " running of " +
			strconv.Itoa(info.Containers),
		strconv.Itoa(info.Images) + " images",
	}
	if info.Escalated {
		parts = append(parts, "reached through the privilege prefix")
	}
	return strings.Join(parts, ", ")
}

// composeCell says whether a project can be driven on this engine, and by what.
func composeCell(info container.EngineInfo) string {
	if !info.Compose {
		return "no compose command answered, so projects cannot be driven here"
	}
	return orNone(info.ComposeVersion)
}

// systemTable renders those rows.
func (a *app) systemTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "", Width: 20},
		{Title: "", Width: 40, Flex: true},
	}
	rows := make([][]string, 0, len(a.system))
	styles := make([]*lipgloss.Style, 0, len(a.system))
	for _, entry := range a.system {
		rows = append(rows, []string{entry.label, entry.value})
		style := a.theme.Row
		if entry.warn {
			style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
		}
		styles = append(styles, &style)
	}
	return columns, rows, styles
}

// containerStyle colours a row by what happened to it, so what is wrong stands
// out from what merely exists.
func (a *app) containerStyle(c container.Container) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case c.Failed():
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case c.Health == container.HealthStarting:
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case c.Running():
		style = a.theme.Row.Foreground(a.theme.OK.GetForeground())
	case c.State == container.StateExited:
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// detailView renders the selected container in full.
func (a *app) detailView() string {
	header := a.headerView()
	tabs := a.tabsView()
	lines := a.detailLines()

	height := a.detailHeight()
	offset := min(a.detailOffset, max(len(lines)-height, 0))
	a.detailOffset = offset
	end := min(offset+height, len(lines))

	body := make([]string, 0, height)
	for _, line := range lines[offset:end] {
		body = append(body, a.theme.Row.Width(a.width).Render(
			ui.Truncate(line, a.width-2)))
	}
	for i := len(body); i < height; i++ {
		body = append(body, a.theme.Row.Width(a.width).Render(""))
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	position := strconv.Itoa(offset+1) + "–" + strconv.Itoa(end) +
		" of " + strconv.Itoa(len(lines)) + " lines  ·  esc to go back"
	status := ui.StatusLine(a.theme, a.statusKind, a.status, position, a.width)
	return strings.Join([]string{header, tabs,
		strings.Join(body, "\n"), help, status}, "\n")
}

// detailLines builds the detail screen's text: what the container is, what it
// is using, what it is mounted on, and what its health check has been saying.
func (a *app) detailLines() []string {
	c := a.detail.Container
	if c.ID == "" {
		return []string{"(nothing selected)"}
	}

	lines := []string{
		c.Name,
		"",
		"  engine         " + c.Target.String(),
		"  id             " + c.ID,
		"  image          " + orNone(c.Image),
		"  command        " + orNone(c.Command),
		"  state          " + orNone(string(c.State)) + "   " + orNone(c.Status),
		"  health         " + healthLine(c),
		"  created        " + absolute(c.Created),
		"  started        " + absolute(c.Started),
		"  exit status    " + exitLine(c),
		"  restart        " + restartLine(c),
	}
	if c.Project != "" {
		lines = append(lines,
			"  compose        "+c.Project+" / "+orNone(c.Service),
			"  project dir    "+orNone(c.WorkingDir))
	}
	if ports := c.PortsText(); ports != "" {
		lines = append(lines, "  ports          "+ports)
	}

	if a.detailErr != "" {
		return append(lines, "", "The inspect failed", "  "+a.detailErr)
	}
	if a.detailLoad {
		return append(lines, "", "reading the container in full…")
	}

	lines = append(lines, a.statsSection()...)
	lines = append(lines, a.limitsSection()...)
	lines = append(lines, a.networksSection()...)
	lines = append(lines, a.mountsSection()...)
	lines = append(lines, a.healthSection()...)
	lines = append(lines, a.envSection()...)
	return append(lines, "", a.actionHint(c))
}

// healthLine renders the health verdict, and says plainly when there is no
// check rather than implying the container is well.
func healthLine(c container.Container) string {
	switch c.Health {
	case container.HealthNone:
		return "no health check is declared, so nothing has judged it"
	case container.HealthUnhealthy:
		return "unhealthy — its own check has been failing"
	case container.HealthStarting:
		return "starting — inside the check's start period, not yet counted"
	default:
		return string(c.Health)
	}
}

// exitLine renders how the last run ended, distinguishing "exited 0" from
// "nobody read a code".
func exitLine(c container.Container) string {
	if !c.ExitCodeKnown {
		return "— (the engine reported none)"
	}
	code := strconv.Itoa(c.ExitCode)
	switch c.ExitCode {
	case 0:
		return "0 — it finished on its own"
	case 137:
		return "137 — killed with SIGKILL, which is usually the machine running " +
			"out of memory or a stop that timed out"
	case 143:
		return "143 — stopped with SIGTERM, which is an ordinary stop"
	default:
		return code + " — the process itself decided to exit with this"
	}
}

// restartLine renders the restart policy and how often it has fired.
func restartLine(c container.Container) string {
	policy := c.RestartPolicy
	if policy == "" {
		policy = "— (the engine reported none)"
	}
	if c.RestartCount == 0 {
		return policy
	}
	return policy + ", restarted " + strconv.Itoa(c.RestartCount) + " times"
}

// statsSection is one sample of what the container is using now.
func (a *app) statsSection() []string {
	lines := []string{"", "Using right now"}
	if a.detail.StatsErr != "" {
		return append(lines, "  "+a.detail.StatsErr)
	}
	if a.detail.Stats.Empty() {
		return append(lines, "  (no sample was returned)")
	}
	s := a.detail.Stats
	return append(lines,
		"  cpu            "+orNone(s.CPUPercent),
		"  memory         "+orNone(s.MemUsage)+"   "+orNone(s.MemPercent),
		"  network i/o    "+orNone(s.NetIO),
		"  block i/o      "+orNone(s.BlockIO),
		"  processes      "+orNone(s.PIDs),
		"  one sample, from `stats --no-stream`; nothing is left running")
}

// limitsSection is what the container was allowed, where a zero means no limit
// rather than none allowed.
func (a *app) limitsSection() []string {
	l := a.detail.Limits
	lines := []string{"", "Limits"}
	return append(lines,
		"  memory         "+limitBytes(l.MemoryBytes),
		"  memory soft    "+limitBytes(l.MemoryReservationBytes),
		"  cpu            "+limitCPUs(l.NanoCPUs),
		"  processes      "+limitCount(l.PidsLimit),
		"  a limit of zero is no limit, which is what both engines report for "+
			"a container created without one")
}

// limitBytes renders a memory limit.
func limitBytes(bytes int64) string {
	if bytes <= 0 {
		return "unlimited"
	}
	return humanBytes(bytes)
}

// limitCPUs renders a CPU limit, which both engines store in billionths of a
// core.
func limitCPUs(nano int64) string {
	if nano <= 0 {
		return "unlimited"
	}
	return strconv.FormatFloat(float64(nano)/1e9, 'g', 3, 64) + " cores"
}

// limitCount renders a process limit.
func limitCount(count int64) string {
	if count <= 0 {
		return "unlimited"
	}
	return strconv.FormatInt(count, 10)
}

// humanBytes renders a byte count in binary units, which is what a memory limit
// is set in.
func humanBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + "B"
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	index := -1
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + units[index]
}

// networksSection is which networks the container is on, and at which address.
func (a *app) networksSection() []string {
	lines := []string{"", "Networks"}
	if len(a.detail.Networks) == 0 {
		return append(lines, "  (none: it shares the host's network, or has none)")
	}
	for _, n := range a.detail.Networks {
		lines = append(lines, "  "+n.Name+"   "+orNone(n.IPAddress)+
			"   gateway "+orNone(n.Gateway))
	}
	return lines
}

// mountsSection is what the container has mounted, and whether it can write to
// it.
func (a *app) mountsSection() []string {
	lines := []string{"", "Mounts"}
	if len(a.detail.Mounts) == 0 {
		return append(lines, "  (none: everything it writes is in its own "+
			"writable layer, and goes when it does)")
	}
	for _, m := range a.detail.Mounts {
		access := "read-only"
		if m.RW {
			access = "read-write"
		}
		lines = append(lines, "  "+m.Type+"  "+orNone(m.Source)+
			" → "+m.Destination+"  ("+access+")")
	}
	return lines
}

// healthSection is what the container's own health check has been saying, which
// is the answer to the question an unhealthy row raises.
func (a *app) healthSection() []string {
	report := a.detail.Health
	if report.Status == container.HealthNone && len(report.Log) == 0 {
		return nil
	}
	lines := []string{"", "Health check"}
	lines = append(lines, "  status         "+string(report.Status))
	if report.FailingStreak > 0 {
		lines = append(lines, "  failing streak "+
			strconv.Itoa(report.FailingStreak)+" checks in a row")
	}
	if len(report.Log) == 0 {
		return append(lines, "  (the engine kept no record of the runs)")
	}
	for _, entry := range report.Log {
		when := "—"
		if !entry.End.IsZero() {
			when = entry.End.Local().Format("15:04:05")
		}
		lines = append(lines, "  "+when+"  exit "+strconv.Itoa(entry.ExitCode)+
			"  "+firstLine(strings.TrimSpace(entry.Output)))
	}
	return lines
}

// envSection is the container's environment, with the values whose names say
// they carry a secret replaced.
func (a *app) envSection() []string {
	lines := []string{"", "Environment"}
	if len(a.detail.Env) == 0 {
		return append(lines, "  (none beyond what the image set)")
	}
	masked := 0
	for _, variable := range a.detail.Env {
		lines = append(lines, "  "+variable.Name+"="+variable.Value)
		if variable.Masked {
			masked++
		}
	}
	if masked > 0 {
		lines = append(lines, "  "+strconv.Itoa(masked)+" value(s) hidden: a "+
			"name containing PASS, SECRET, TOKEN, KEY or CREDENTIAL is masked, "+
			"so the name is on screen and the value is not")
	}
	return lines
}

// actionHint is the last line of the detail screen: what can be done to this
// particular container.
func (a *app) actionHint(c container.Container) string {
	if c.Running() {
		return "  x stops · r restarts · K kills · p pauses · o restart policy · " +
			"L reads its log"
	}
	return "  s starts · d removes · o restart policy · U pulls its image · " +
		"L reads its log"
}

// logsView renders the log pane.
func (a *app) logsView() string {
	header := a.headerView()
	tabs := a.tabsView()
	lines := a.logLines()

	height := a.logsHeight()
	offset := min(a.logsOffset, max(len(lines)-height, 0))
	a.logsOffset = offset
	end := min(offset+height, len(lines))

	body := make([]string, 0, height)
	for _, line := range lines[offset:end] {
		body = append(body, a.theme.Row.Width(a.width).Render(
			ui.Truncate(line, a.width-2)))
	}
	for i := len(body); i < height; i++ {
		body = append(body, a.theme.Row.Width(a.width).Render(""))
	}

	help := ui.HelpBar(a.theme, logsHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.logsStatus(len(lines)),
		a.width)
	return strings.Join([]string{header, tabs,
		strings.Join(body, "\n"), help, status}, "\n")
}

// logLines is the pane's content, with a header naming the command that
// produced it.
//
// The command line is on screen for the same reason every other command line in
// this tool is: what you are reading came from somewhere, and the pane says
// where rather than presenting the log as if the tool had produced it.
func (a *app) logLines() []string {
	c, ok := a.selectedContainer()
	if !ok {
		return []string{"(no container selected)"}
	}
	lines := []string{c.Name + "  —  " + c.Target.String(), ""}
	if command := a.backend.LogsCommand(c, a.logsOpts); command != "" {
		lines = append(lines, "$ "+command, "")
	}
	if a.logsErr != "" {
		return append(lines, "The read failed", "  "+a.logsErr)
	}
	text := strings.TrimRight(a.logs, "\n")
	if strings.TrimSpace(text) == "" {
		return append(lines,
			"(nothing in this window)",
			"",
			"A container that has never started has no log at all. One that is",
			"running and quiet has an empty window: widen it with w, or read",
			"more lines with n.")
	}
	return append(lines, strings.Split(text, "\n")...)
}

// logsStatus is the pane's own hint line.
func (a *app) logsStatus(total int) string {
	parts := []string{strconv.Itoa(total) + " lines"}
	parts = append(parts, "last "+strconv.Itoa(a.logsOpts.Tail))
	if a.logsOpts.Since != "" {
		parts = append(parts, "since "+a.logsOpts.Since)
	}
	if a.logsOpts.Timestamps {
		parts = append(parts, "timestamps on")
	}
	if a.logsFollow {
		parts = append(parts, "following, re-read every "+followInterval.String())
	}
	return strings.Join(parts, "  ·  ") + "  ·  esc to go back"
}

// absolute renders a moment in full, or a placeholder when there is none.
func absolute(when time.Time) string {
	if when.IsZero() {
		return "—"
	}
	return when.Local().Format("Mon 2006-01-02 15:04:05 MST") +
		"   (" + relative(when) + ")"
}

// relative renders how long ago a moment was.
func relative(when time.Time) string {
	if when.IsZero() {
		return "—"
	}
	delta := time.Since(when)
	future := delta < 0
	if future {
		delta = -delta
	}
	unit := shortDuration(delta)
	if future {
		return "in " + unit
	}
	return unit + " ago"
}

// shortDuration renders a duration in the one unit that fits a column.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "min"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	default:
		return strconv.Itoa(int(d.Hours()/24/365)) + "y"
	}
}

// yesNo renders a boolean in words.
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// orNone renders an empty value as a visible placeholder, so a blank cell is
// never mistaken for a missing read.
func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// shortHelpKeys is the single-line hint bar, which changes with the screen
// because the keys that do anything change with it.
func (a *app) shortHelpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{{Key: "tab", Desc: "screen"}}
	switch a.screen {
	case screenImages:
		hints = append(hints,
			ui.KeyHint{Key: "P", Desc: "pull"},
			ui.KeyHint{Key: "N", Desc: "run"},
			ui.KeyHint{Key: "d", Desc: "remove"},
			ui.KeyHint{Key: "X", Desc: "prune"})
	case screenStorage:
		hints = append(hints,
			ui.KeyHint{Key: "n", Desc: "create"},
			ui.KeyHint{Key: "d", Desc: "remove"},
			ui.KeyHint{Key: "X", Desc: "prune"})
	case screenSystem:
		hints = append(hints,
			ui.KeyHint{Key: "X", Desc: "prune"},
			ui.KeyHint{Key: "A", Desc: "auto-update"})
	default:
		hints = append(hints,
			ui.KeyHint{Key: "enter", Desc: "detail"},
			ui.KeyHint{Key: "N", Desc: "run"},
			ui.KeyHint{Key: "L", Desc: "log"},
			ui.KeyHint{Key: "s/x", Desc: "start/stop"},
			ui.KeyHint{Key: "r", Desc: "restart"},
			ui.KeyHint{Key: "d", Desc: "remove"},
			ui.KeyHint{Key: "c", Desc: "compose"})
	}
	return append(hints,
		ui.KeyHint{Key: "/", Desc: "filter"},
		ui.KeyHint{Key: "?", Desc: "help"},
		ui.KeyHint{Key: "q", Desc: "quit"})
}

// logsHelpKeys is the hint bar of the log pane, whose keys are its own.
func logsHelpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "f", Desc: "follow"},
		{Key: "t", Desc: "timestamps"},
		{Key: "n", Desc: "lines"},
		{Key: "w", Desc: "window"},
		{Key: "G", Desc: "end"},
		{Key: "esc", Desc: "back"},
	}
}

// helpKeys is the full key list shown on the help screen.
//
// It is kept to what fits a 26-row terminal, because a help screen whose first
// lines are off the top is a help screen that hides the navigation keys — the
// ones a reader who opened it most likely came for.
func helpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "tab / 1-4", Desc: "containers, images, volumes & networks, engines"},
		{Key: "↑k ↓j g G", Desc: "move the selection; pgup/pgdn scroll a page"},
		{Key: "enter", Desc: "open the selected container: what it is using, its mounts, its health"},
		{Key: "L", Desc: "read its log in a pane (esc leaves either pane)"},
		{Key: "/", Desc: "filter this screen (esc clears)"},
		{Key: "s / x", Desc: "start / stop the selected container"},
		{Key: "r / K", Desc: "restart it / kill it — SIGKILL, no grace period"},
		{Key: "p / u", Desc: "pause / unpause it"},
		{Key: "d", Desc: "remove what is selected: a container, image, volume or network"},
		{Key: "o", Desc: "change the selected container's restart policy"},
		{Key: "U", Desc: "pull its image (the container itself is not recreated)"},
		{Key: "N", Desc: "run a new container from an image, detached, previewed first"},
		{Key: "n", Desc: "on screen 3: create a volume or a network"},
		{Key: "P", Desc: "on screen 2: pull an image by reference"},
		{Key: "c", Desc: "compose up, down or pull for its project"},
		{Key: "X", Desc: "prune, with each variant spelled out before you choose"},
		{Key: "A", Desc: "what Podman's auto-update would do, as a dry run"},
		{Key: "R", Desc: "re-read every engine"},
		{Key: "? / q", Desc: "this help / quit"},
		{Key: "", Desc: ""},
		{Key: "in a log", Desc: "f follows, t timestamps, n how many lines, w the time window"},
		{Key: "note", Desc: "every change is previewed as its exact command line and confirmed"},
		{Key: "note", Desc: "there is no exec, and a log is re-read rather than followed: this tool starts no process it does not wait for"},
	}
}
