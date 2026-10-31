package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
	"github.com/RashKash103/satellite/internal/view"
)

// viewsCmd: sat PROJECT views: the project's views, one per row.
func viewsCmd(p *conf.Project) int {
	t := ui.Out()
	vs := view.List(p)
	if len(vs) == 0 {
		t.Info("%s has no views (executables in its bundle's sat/views/: docs/projects.md)", p.Name)
		return 0
	}
	var rows [][]string
	bad := 0
	for _, v := range vs {
		mode := v.Mode
		if t.Color() && v.Mode == view.ModeSnapshot {
			mode += t.Muted(", every " + secs(v.Every))
		}
		title := v.Title
		if v.Err != nil {
			bad++
			title = t.Bad("(bad header)")
		}
		rows = append(rows, []string{t.Accent(v.Name), title, v.Scope, mode, v.Format})
	}
	t.Print(t.Table("", []string{"NAME", "TITLE", "SCOPE", "MODE", "FORMAT"}, rows))
	for _, v := range vs {
		if v.Err != nil {
			warnf("sat/views/%s: bad header: %v", v.Name, v.Err)
		}
	}
	if t.Color() {
		t.Println(t.Muted("sat " + p.Name + " view NAME [--lane N] [-w] shows one"))
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func secs(d time.Duration) string { return view.Secs(d) }

// laneFor: the lane a lane view looks at: --lane N, else the only busy one.
func laneFor(p *conf.Project, want int) (*view.Lane, error) {
	st, err := agent.ReadState(p)
	if err != nil || !st.Live() {
		return nil, fmt.Errorf("%s is not running: a lane view needs a lane with a job", p.Name)
	}
	var busy []agent.Lane
	for _, l := range st.Lanes {
		if l.ID != "" {
			busy = append(busy, l)
		}
		if want > 0 && l.Slot == want {
			if l.ID == "" {
				return nil, fmt.Errorf("lane %d has no job now", want)
			}
			return view.LaneOf(p, l), nil
		}
	}
	switch {
	case want > 0:
		return nil, fmt.Errorf("%s has no lane %d", p.Name, want)
	case len(busy) == 1:
		return view.LaneOf(p, busy[0]), nil
	case len(busy) == 0:
		return nil, errors.New("no lane has a job now")
	}
	var ns []string
	for _, l := range busy {
		ns = append(ns, strconv.Itoa(l.Slot))
	}
	return nil, fmt.Errorf("lanes %s have jobs: which one? --lane N", strings.Join(ns, " "))
}

// viewCmd: sat PROJECT view NAME [--lane N] [-w|--watch].
func viewCmd(p *conf.Project, a []string) int {
	const use = "usage: sat [PROJECT] view NAME [--lane N] [-w|--watch]"
	watch, a := flag(a, "-w", "--watch")
	lane := 0
	var rest []string
	for i := 0; i < len(a); i++ {
		x := a[i]
		v, isEq := strings.CutPrefix(x, "--lane=")
		if x == "--lane" {
			if i+1 >= len(a) {
				return fail(use)
			}
			v, isEq, i = a[i+1], true, i+1
		}
		if isEq {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fail("--lane wants a lane number, from 1")
			}
			lane = n
			continue
		}
		rest = append(rest, x)
	}
	if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
		return fail(use)
	}
	v, ok := view.Find(p, rest[0])
	if !ok {
		return failHint(p.Name+" has no view "+rest[0], "sat "+p.Name+" views lists them")
	}
	if v.Err != nil {
		return fail("sat/views/" + v.Name + ": bad header: " + v.Err.Error())
	}
	t := ui.Out()
	o := view.Opts{Live: watch, Width: t.Width(), Color: t.Color()}
	switch {
	case v.Scope == view.ScopeLane:
		l, err := laneFor(p, lane)
		if err != nil {
			return fail(err.Error())
		}
		o.Lane = l
	case lane > 0:
		return fail(v.Name + " is a project view: --lane is for lane views")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hist := view.LoadHist(view.HistFile(p, v.Name))
	r := view.Render{Width: o.Width, Color: o.Color, S: t.S, Hist: hist}
	switch {
	case !watch:
		return viewOnce(ctx, p, v, o, r)
	case v.Mode == view.ModeSnapshot:
		return viewWatch(ctx, p, v, o, r, hist)
	}
	return viewFollow(ctx, p, v, o, r, hist)
}

// viewOnce: run once and print what it showed.
func viewOnce(ctx context.Context, p *conf.Project, v view.View, o view.Opts, r view.Render) int {
	t, e := ui.Out(), ui.Err()
	var err error
	if v.Format == view.FormatJSON {
		var out bytes.Buffer
		err = view.Run(ctx, p, v, o, &out, view.Writer(e, e.Color()))
		if d, perr := view.Parse(out.Bytes()); perr == nil {
			for _, l := range r.Doc(d) {
				t.Println(l)
			}
		} else if err == nil {
			return fail("view " + v.Name + ": " + perr.Error())
		}
	} else {
		w, ew := view.Writer(t, o.Color), view.Writer(e, e.Color())
		err = view.Run(ctx, p, v, o, w, ew)
		w.Flush()
		ew.Flush()
	}
	return viewErr(v, err)
}

func viewErr(v view.View, err error) int {
	var te view.TimeoutError
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return 0
	case errors.As(err, &te):
		failHint("view "+v.Name+" "+te.Error(), "its header's timeout= is "+secs(v.Timeout))
		return 1
	}
	if c := code(err); c != 0 {
		return c
	}
	return 1
}

// frame: one run's lines, or the error that stood in for them.
func frame(ctx context.Context, p *conf.Project, v view.View, o view.Opts, r view.Render, hist *view.Hist) []string {
	var out, errb bytes.Buffer
	err := view.Run(ctx, p, v, o, &out, &errb)
	var ls []string
	if v.Format == view.FormatJSON {
		d, perr := view.Parse(out.Bytes())
		switch {
		case perr == nil:
			hist.Record(d)
			ls = r.Doc(d)
		case err == nil:
			ls = r.Error(perr.Error())
		}
	} else {
		w := &view.Lines{Color: o.Color, F: func(l string) { ls = append(ls, l) }}
		w.Write(out.Bytes())
		w.Flush()
	}
	el := &view.Lines{Color: o.Color, F: func(l string) { ls = append(ls, l) }}
	el.Write(errb.Bytes())
	el.Flush()
	if err != nil && ctx.Err() == nil {
		ls = append(ls, r.Error(view.Describe(err))...)
	}
	return ls
}

// screen draws frames in place on a terminal, or one after another elsewhere.
type screen struct {
	t     *ui.T
	live  bool
	title string
	first bool
}

func newScreen(v view.View) *screen { return newScreenTitled(v.Title) }

func newScreenTitled(title string) *screen {
	t := ui.Out()
	return &screen{t: t, live: t.Live() && t.Color() && term.IsTerminal(int(os.Stdout.Fd())), title: title, first: true}
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (s *screen) draw(ls []string, note string) {
	t := s.t
	stamp := time.Now().Format("15:04:05")
	if !s.live {
		t.Println("== " + s.title + " " + stamp)
		for _, l := range ls {
			t.Println(l)
		}
		return
	}
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		w, h = 80, 24
	}
	head := t.Bold(s.title) + t.Muted(" · "+note+" · "+stamp+" · ctrl+c stops")
	all := append([]string{ui.Truncate(head, w)}, ls...)
	if len(all) > h-1 {
		all = append(all[:h-2], t.Muted(fmt.Sprintf("… %d more lines", len(all)-(h-2))))
	}
	var b strings.Builder
	if s.first {
		b.WriteString("\x1b[2J")
		s.first = false
	}
	b.WriteString("\x1b[H")
	for _, l := range all {
		b.WriteString(ui.Truncate(l, w) + "\x1b[K\n")
	}
	b.WriteString("\x1b[J")
	t.Print(b.String())
}

// viewWatch: a snapshot view, run every `every` seconds until ctrl+c.
func viewWatch(ctx context.Context, p *conf.Project, v view.View, o view.Opts, r view.Render, hist *view.Hist) int {
	s := newScreen(v)
	for ctx.Err() == nil {
		ls := frame(ctx, p, v, o, r, hist)
		if ctx.Err() != nil {
			break
		}
		hist.Save(view.HistFile(p, v.Name))
		s.draw(ls, "every "+secs(v.Every))
		select {
		case <-ctx.Done():
		case <-time.After(v.Every):
		}
	}
	return 0
}

// viewFollow: a stream view, followed until it ends or ctrl+c.
func viewFollow(ctx context.Context, p *conf.Project, v view.View, o view.Opts, r view.Render, hist *view.Hist) int {
	t, e := ui.Out(), ui.Err()
	ew := view.Writer(e, e.Color())
	var w *view.Lines
	if v.Format == view.FormatJSON {
		s := newScreen(v)
		w = &view.Lines{Color: o.Color, F: func(l string) {
			if strings.TrimSpace(l) == "" {
				return
			}
			d, err := view.Parse([]byte(l))
			if err != nil {
				s.draw(r.Error(err.Error()), "following")
				return
			}
			hist.Record(d)
			hist.Save(view.HistFile(p, v.Name))
			s.draw(r.Doc(d), "following")
		}}
	} else {
		w = view.Writer(t, o.Color)
	}
	err := view.Run(ctx, p, v, o, w, ew)
	w.Flush()
	ew.Flush()
	if ctx.Err() != nil {
		return 0
	}
	return viewErr(v, err)
}
