package tui

import (
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

// setupForm: `sat setup [--install] HUB_IP PROJECT [HUB_USER]` as a form.
type setupForm struct {
	in      [3]textinput.Model // hub IP, project, hub user
	install bool
	at      int // 0-2 the inputs, 3 the install choice
	err     string
	st      ui.Styles
}

var formLabels = [3]string{"Hub IP", "Project", "Hub user"}

func newSetupForm(st ui.Styles) *setupForm {
	f := &setupForm{st: st}
	for i, ph := range []string{"192.0.2.10", "the project's name on the hub", os.Getenv("USER")} {
		f.in[i] = textinput.New()
		f.in[i].Prompt = ""
		f.in[i].Placeholder = ph
		f.in[i].SetWidth(32)
	}
	return f
}

func (f *setupForm) focus() tea.Cmd {
	var c tea.Cmd
	for i := range f.in {
		if i == f.at {
			c = f.in[i].Focus()
		} else {
			f.in[i].Blur()
		}
	}
	return c
}

func (f *setupForm) update(msg tea.Msg) tea.Cmd {
	if f.at > 2 {
		return nil
	}
	var c tea.Cmd
	f.in[f.at], c = f.in[f.at].Update(msg)
	return c
}

// args: the setup command line, or why it can't run yet.
func (f *setupForm) args() ([]string, string) {
	ip, proj, user := strings.TrimSpace(f.in[0].Value()), strings.TrimSpace(f.in[1].Value()), strings.TrimSpace(f.in[2].Value())
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
	if f.install {
		a = append(a, "--install")
	}
	a = append(a, ip, proj)
	if user != "" {
		a = append(a, user)
	}
	return a, ""
}

func (m *model) formKey(msg tea.KeyPressMsg) tea.Cmd {
	f := m.form
	switch msg.String() {
	case "esc":
		m.form = nil
		m.say("no project added", false)
		return nil
	case "tab", "down":
		f.at = (f.at + 1) % 4
		return f.focus()
	case "shift+tab", "up":
		f.at = (f.at + 3) % 4
		return f.focus()
	case "space":
		if f.at == 3 {
			f.install = !f.install
			return nil
		}
	case "enter":
		if f.at < 3 {
			f.at++
			return f.focus()
		}
		a, why := f.args()
		if why != "" {
			f.err = why
			return nil
		}
		m.form = nil
		title := "setup " + strings.TrimSpace(f.in[1].Value())
		if f.install {
			// It installs only after showing every step and asking: on the terminal.
			return m.terminal(title, a...)
		}
		return m.stream(title, a...)
	}
	return f.update(msg)
}

func (f *setupForm) view(w int) []string {
	t := tstyle(f.st)
	lines := []string{
		t.muted.Render("sat setup: a key for the hub, the service,"),
		t.muted.Render("the bundle and a check. Nothing starts until s."),
		t.muted.Render("Log in to the hub with ssh once first."),
		"",
	}
	for i := range f.in {
		f.in[i].SetWidth(max(w-14, 10))
		mark := "  "
		if f.at == i {
			mark = t.accent.Render("> ")
		}
		lines = append(lines, mark+ui.Pad(formLabels[i], 10)+f.in[i].View())
	}
	box := "[ ]"
	if f.install {
		box = "[x]"
	}
	mark := "  "
	if f.at == 3 {
		mark = t.accent.Render("> ")
	}
	lines = append(lines, mark+ui.Pad("Install", 10)+box+t.muted.Render(" what is missing (space; asks first)"), "")
	if f.err != "" {
		lines = append(lines, t.bad.Render(f.err), "")
	}
	lines = append(lines, t.muted.Render("tab next · enter on the last: set up · esc"))
	for i := range lines {
		lines[i] = ui.Truncate(lines[i], w)
	}
	return lines
}
