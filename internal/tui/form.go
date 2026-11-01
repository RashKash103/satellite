package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/conf"
)

// The live view's dialogs, built on modal: confirmations, the setup form, the env prompt, quitting
// while an action runs, and the help. The drain schedule's is in schedule.go.

// confirmDialog: a yes/no question. A dangerous one starts on Cancel, as [y/N] did: enter alone
// does nothing; y (or the button) does it, n or esc doesn't.
func (m *model) confirmDialog(title string, body []string, yes string, danger bool, do func() tea.Cmd) *modal {
	d := &modal{kind: "confirm", title: title, body: body, width: 46,
		buttons: []button{
			{label: yes, keys: []string{"y", "Y"}, danger: danger, do: func(*modal) (tea.Cmd, string) { return do(), "" }},
			{label: "Cancel", keys: []string{"n", "N"}, cancel: true, do: func(*modal) (tea.Cmd, string) {
				m.say("nothing was done", false)
				return nil, ""
			}},
		}}
	if danger {
		d.focus = 1
	}
	return d
}

// setupDialog: `sat setup [--install] HUB_IP PROJECT [HUB_USER]` as a form.
func (m *model) setupDialog() *modal {
	d := &modal{kind: "setup", title: "Add a project", width: 56,
		body: []string{"sat setup: a key for the hub, the service, the bundle and a check. Nothing starts until s. Log in to the hub with ssh once first."},
		fields: []*field{
			textField("Hub IP", "192.0.2.10", ""),
			textField("Project", "the project's name on the hub", ""),
			textField("Hub user", os.Getenv("USER"), ""),
			checkField("Install", "what is missing (asks first)", false),
		}}
	d.buttons = []button{
		{label: "Set up", do: func(d *modal) (tea.Cmd, string) {
			a, why := setupArgs(d)
			if why != "" {
				return nil, why
			}
			title := "setup " + d.fields[1].value()
			if d.fields[3].on {
				// It installs only after showing every step and asking: on the terminal.
				return m.terminal(title, a...), ""
			}
			return m.stream(title, a...), ""
		}},
		{label: "Cancel", cancel: true, do: func(*modal) (tea.Cmd, string) {
			m.say("no project added", false)
			return nil, ""
		}},
	}
	return d
}

// setupArgs: the setup command line, or why it can't run yet.
func setupArgs(d *modal) ([]string, string) {
	ip, proj, user := d.fields[0].value(), d.fields[1].value(), d.fields[2].value()
	switch {
	case ip == "":
		return nil, "the hub's IP address is needed"
	case !conf.NameOK(ip) && !strings.Contains(ip, ":"):
		return nil, "not an IP address or host name: " + ip
	case !conf.NameOK(proj):
		return nil, "a project name: letters, digits, . _ - only"
	case user != "" && !conf.NameOK(user):
		return nil, "a user name: letters, digits, . _ - only"
	}
	a := []string{"setup"}
	if d.fields[3].on {
		a = append(a, "--install")
	}
	a = append(a, ip, proj)
	if user != "" {
		a = append(a, user)
	}
	return a, ""
}

// envDialog: set or unset one of the project's env keys (sat PROJECT env KEY=VALUE | -KEY).
func (m *model) envDialog(p *proj) *modal {
	n := p.p.Name
	d := &modal{kind: "env", title: "env · " + n, width: 56,
		body:   []string{"KEY=VALUE sets a key in the project's env file, -KEY unsets it. Every job sources the file, so a change applies from the next job."},
		fields: []*field{textField("Set", "KEY=VALUE or -KEY", "")}}
	d.buttons = []button{
		{label: "Apply", do: func(d *modal) (tea.Cmd, string) {
			v := d.fields[0].value()
			k, _, _ := strings.Cut(strings.TrimPrefix(v, "-"), "=")
			if k == "" || strings.ContainsAny(k, " \t") {
				return nil, "KEY=VALUE to set, -KEY to unset"
			}
			return m.stream("env "+v, n, "env", v), ""
		}},
		{label: "Cancel", cancel: true},
	}
	return d
}

// quitDialog: q while an action still runs.
func (m *model) quitDialog() *modal {
	return &modal{kind: "quit", title: "Quit?", width: 46,
		body: []string{m.job.title + " is still running. Quitting now may cut it short."},
		buttons: []button{
			{label: "Quit", keys: []string{"y", "Y", "q", "ctrl+c"}, do: func(*modal) (tea.Cmd, string) {
				m.stopRun()
				return tea.Quit, ""
			}},
			{label: "Stay", keys: []string{"n", "N"}, cancel: true},
		}, focus: 1}
}

// helpDialog: every key, scrolled with j/k when it doesn't fit.
func (m *model) helpDialog() *modal {
	d := &modal{kind: "help", title: "Keys", width: 96, closeKeys: []string{"?", "q"},
		buttons: []button{{label: "Close", cancel: true}}}
	d.render = func(d *modal, t styles, w int) []string { return m.helpLines(t, w) }
	return d
}
