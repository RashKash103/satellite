package view

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/barchart"
	"github.com/NimbleMarkets/ntcharts/v2/canvas"
	"github.com/NimbleMarkets/ntcharts/v2/linechart"
	"github.com/NimbleMarkets/ntcharts/v2/sparkline"

	"github.com/RashKash103/satellite/internal/ui"
)

// Render draws documents in w columns: in sat's colours and with charts when Color, else as
// plain text that reads the same in a pipe or a log.
type Render struct {
	Width int
	Color bool
	S     ui.Styles
	Hist  *Hist     // a metric's earlier values (nil: just its value)
	Now   time.Time // for timelines (zero: time.Now)
}

func (r Render) st(s lipgloss.Style, v string) string {
	if !r.Color || v == "" {
		return v
	}
	return s.Render(v)
}

func (r Render) w() int {
	if r.Width <= 0 {
		return 80
	}
	return r.Width
}

func (r Render) now() time.Time {
	if r.Now.IsZero() {
		return time.Now()
	}
	return r.Now
}

// Doc: the document's lines; its alerts first.
func (r Render) Doc(d *Doc) []string {
	var out []string
	var rest []Block
	for _, b := range d.Blocks {
		if b.Type == "alert" {
			out = append(out, r.Block(b)...)
		} else {
			rest = append(rest, b)
		}
	}
	for i, b := range rest {
		if i > 0 || len(out) > 0 {
			if b.Type != "text" || i > 0 && rest[i-1].Type != "text" {
				out = append(out, "")
			}
		}
		out = append(out, r.Block(b)...)
	}
	if r.Color { // on a screen, never wider than it (plain text may run on, for a pipe)
		for i := range out {
			out[i] = ui.Truncate(out[i], r.w())
		}
	}
	return out
}

// Error: a run that went wrong, in place of the view.
func (r Render) Error(msg string) []string {
	if !r.Color {
		return []string{"error: " + msg}
	}
	return []string{r.st(r.S.Bad, ui.MarkBad+" "+msg)}
}

func (r Render) title(t string) []string {
	if t == "" {
		return nil
	}
	return []string{r.st(r.S.Heading, t)}
}

// Block: one block's lines.
func (r Render) Block(b Block) []string {
	switch b.Type {
	case "text":
		ls := b.Lines
		if b.Text != "" {
			ls = append(strings.Split(strings.TrimRight(b.Text, "\n"), "\n"), ls...)
		}
		out := r.title(b.Title)
		for _, l := range ls {
			out = append(out, Clean(l, r.Color))
		}
		return out
	case "kv":
		var rows [][]string
		for _, row := range b.Rows {
			k, v := "", ""
			if len(row) > 0 {
				k = Clean(cell(row[0]), false)
			}
			if len(row) > 1 {
				v = Clean(cell(row[1]), r.Color)
			}
			rows = append(rows, []string{r.st(r.S.Muted, k), v})
		}
		return append(r.title(b.Title), r.cols("", rows)...)
	case "table":
		var rows [][]string
		if len(b.Columns) > 0 {
			h := make([]string, len(b.Columns))
			for i, c := range b.Columns {
				h[i] = r.st(r.S.Muted.Bold(true), Clean(c, false))
			}
			rows = append(rows, h)
		}
		for _, row := range b.Rows {
			c := make([]string, len(row))
			for i, v := range row {
				c[i] = Clean(cell(v), r.Color)
			}
			rows = append(rows, c)
		}
		return append(r.title(b.Title), r.cols("", rows)...)
	case "progress":
		return []string{r.progress(Clean(b.Label, false), *b.Value, Clean(b.Note, false))}
	case "bar":
		return append(r.title(b.Title), r.bars(b.Data)...)
	case "timeline":
		return append(r.title(b.Title), r.timeline(b.Items)...)
	case "metric":
		return []string{r.metric(b)}
	case "line":
		return append(r.title(b.Title), r.line(b.Series)...)
	case "alert":
		t := Clean(b.Text, false)
		if !r.Color {
			return []string{b.Level + ": " + t}
		}
		if b.Level == "error" {
			return []string{r.st(r.S.Bad.Bold(true), ui.MarkBad+" "+t)}
		}
		return []string{r.st(r.S.Warn.Bold(true), ui.MarkWarn+" "+t)}
	}
	return []string{r.st(r.S.Muted, fmt.Sprintf("(a %q block: this sat can't show it)", b.Type))}
}

func (r Render) cols(indent string, rows [][]string) []string {
	s := strings.TrimRight(ui.Columns(indent, rows), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func pct(v float64) int { return int(math.Round(math.Min(math.Max(v, 0), 1) * 100)) }

func (r Render) progress(label string, v float64, note string) string {
	p := pct(v)
	if !r.Color {
		s := fmt.Sprintf("%s %d%%", label, p)
		if note != "" {
			s += " " + note
		}
		return strings.TrimSpace(s)
	}
	n := min(max(r.w()-ui.Width(label)-ui.Width(note)-8, 6), 30)
	return strings.TrimSpace(label + "  " + Gauge(r.S, v, n) + fmt.Sprintf(" %3d%%", p) + "  " + r.st(r.S.Muted, note))
}

// Gauge: a bar of n cells filled to v (0..1), in sat's bar colour.
func Gauge(s ui.Styles, v float64, n int) string {
	f := pct(v) * n / 100
	return lipgloss.NewStyle().Foreground(s.Bar).Render(strings.Repeat("█", f)) + s.Muted.Render(strings.Repeat("░", n-f))
}

func (r Render) bars(data []Datum) []string {
	if len(data) == 0 {
		return []string{r.st(r.S.Muted, "(no data)")}
	}
	lw, vw := 0, 0
	for _, d := range data {
		lw = max(lw, ui.Width(Clean(d.Label, false)))
		vw = max(vw, len(Num(d.Value)))
	}
	lw = min(lw, max(r.w()/3, 4))
	if !r.Color || r.w() < lw+vw+8 {
		var rows [][]string
		for _, d := range data {
			rows = append(rows, []string{Clean(d.Label, false), Num(d.Value)})
		}
		return r.cols("", rows)
	}
	cw := r.w() - vw - 1
	var bd []barchart.BarData
	neg := false
	for _, d := range data {
		l := []rune(Clean(d.Label, false))
		if len(l) > lw {
			l = append(l[:lw-1], '…')
		}
		bd = append(bd, barchart.BarData{Label: ui.Pad(string(l), lw) + " ", Values: []barchart.BarValue{{Name: string(l), Value: d.Value, Style: lipgloss.NewStyle().Foreground(r.S.Bar)}}})
		neg = neg || d.Value < 0
	}
	bc := barchart.New(cw, len(data), barchart.WithNoAutoBarWidth(), barchart.WithBarWidth(1), barchart.WithBarGap(0),
		barchart.WithStyles(r.S.Muted, lipgloss.NewStyle()), barchart.WithDataSet(bd))
	bc.SetHorizontal(true) // after the data: it sizes the label column
	if !neg {
		bc.SetMin(0)
	}
	bc.Draw()
	ls := strings.Split(bc.View(), "\n")
	out := make([]string, 0, len(data))
	for i, d := range data {
		l := ""
		if i < len(ls) {
			l = ls[i]
		}
		out = append(out, ui.Pad(l, cw)+" "+fmt.Sprintf("%*s", vw, Num(d.Value)))
	}
	return out
}

var stateMark = map[string]string{"done": "✓", "running": "▶", "failed": "✗", "waiting": "…", "": " "}

func (r Render) timeline(items []Item) []string {
	if len(items) == 0 {
		return []string{r.st(r.S.Muted, "(nothing yet)")}
	}
	now := float64(r.now().Unix())
	lo, hi := math.Inf(1), math.Inf(-1)
	lw := 0
	for _, it := range items {
		lw = max(lw, ui.Width(Clean(it.Label, false)))
		end := it.End
		if end == 0 {
			end = math.Max(now, it.Start)
		}
		if it.Start > 0 {
			lo = math.Min(lo, it.Start)
		}
		hi = math.Max(hi, end)
	}
	lw = min(lw, max(r.w()/3, 4))
	dur := func(it Item) string {
		end := it.End
		if end == 0 {
			end = now
		}
		if it.Start <= 0 || end < it.Start {
			return ""
		}
		return ui.Dur(time.Duration((end - it.Start) * float64(time.Second)))
	}
	clock := func(f float64) string {
		if f <= 0 {
			return "-"
		}
		return time.Unix(int64(f), 0).Format("15:04:05")
	}
	if !r.Color {
		var rows [][]string
		for _, it := range items {
			span := clock(it.Start)
			if it.Start > 0 {
				span += "-" + map[bool]string{true: clock(it.End), false: ""}[it.End > 0]
			}
			d := dur(it)
			if d != "" {
				d = "(" + d + ")"
			}
			st := it.State
			if st == "" {
				st = "-"
			}
			rows = append(rows, []string{Clean(it.Label, false), st, span, d})
		}
		return r.cols("", rows)
	}
	track := r.w() - lw - 2 - 2 - 9
	if track < 8 || math.IsInf(lo, 1) || hi <= lo {
		track = 0
	}
	var out []string
	for _, it := range items {
		l := ui.Pad(ui.Truncate(Clean(it.Label, false), lw), lw)
		sty := map[string]lipgloss.Style{"done": r.S.OK, "running": r.S.Accent, "failed": r.S.Bad, "waiting": r.S.Muted, "": r.S.Muted}[it.State]
		s := l + " " + r.st(sty, stateMark[it.State]) + " "
		if track > 0 {
			end := it.End
			if end == 0 {
				end = math.Max(now, it.Start)
			}
			a, b := 0, 0
			if it.Start > 0 {
				a = int(float64(track) * (it.Start - lo) / (hi - lo))
				b = int(math.Ceil(float64(track) * (end - lo) / (hi - lo)))
			}
			a, b = min(max(a, 0), track-1), min(max(b, a+1), track)
			ch := "█"
			if it.State == "waiting" || it.Start <= 0 {
				ch = "░"
			}
			s += r.st(r.S.Muted, strings.Repeat("·", a)) + r.st(sty, strings.Repeat(ch, b-a)) + r.st(r.S.Muted, strings.Repeat("·", track-b)) + " "
		}
		out = append(out, s+r.st(r.S.Muted, fmt.Sprintf("%8s", dur(it))))
	}
	return out
}

func span(vs []float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, v := range vs {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return
}

func (r Render) metric(b Block) string {
	name, unit := Clean(b.Name, false), Clean(b.Unit, false)
	val := Num(*b.Value)
	if unit != "" {
		val += " " + unit
	}
	vs := r.Hist.Get(b.Name)
	if len(vs) == 0 || vs[len(vs)-1] != *b.Value {
		vs = append(vs, *b.Value)
	}
	lo, hi := span(vs)
	rng := ""
	if len(vs) > 1 {
		rng = fmt.Sprintf("min %s, max %s", Num(lo), Num(hi))
	}
	if !r.Color {
		if rng != "" {
			return fmt.Sprintf("%s %s (%s over %d)", name, val, rng, len(vs))
		}
		return name + " " + val
	}
	s := r.st(r.S.Muted, name) + "  " + r.st(r.S.Bold, val)
	if n := r.w() - ui.Width(s) - ui.Width(rng) - 4; n >= 8 && len(vs) > 1 {
		s += "  " + Spark(r.S, vs, min(n, 60))
	}
	if rng != "" {
		s += "  " + r.st(r.S.Muted, rng)
	}
	return s
}

// Spark: the last w values as a one-line sparkline, scaled from 0 to their maximum (or top, if
// larger), in sat's bar colour.
func Spark(s ui.Styles, vs []float64, w int, top ...float64) string {
	return strings.Join(SparkH(s, vs, w, 1, top...), "")
}

// SparkH: Spark, h rows high.
func SparkH(s ui.Styles, vs []float64, w, h int, top ...float64) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	if len(vs) > w {
		vs = vs[len(vs)-w:]
	}
	sl := sparkline.New(w, h, sparkline.WithStyle(lipgloss.NewStyle().Foreground(s.Bar)))
	_, hi := span(vs)
	for _, t := range top {
		hi = math.Max(hi, t)
	}
	if hi > 0 {
		sl.SetMax(hi)
	}
	sl.AutoMaxValue = false
	sl.PushAll(vs)
	sl.DrawColumnsOnly()
	return strings.Split(sl.View(), "\n")
}

// palette for line series, after the bar colour.
func (r Render) seriesStyle(i int) lipgloss.Style {
	return []lipgloss.Style{lipgloss.NewStyle().Foreground(r.S.Bar), r.S.OK, r.S.Warn, r.S.Bad, r.S.Muted}[i%5]
}

func (r Render) line(series []Series) []string {
	var all []float64
	var xs []float64
	for _, s := range series {
		for _, p := range s.Points {
			xs = append(xs, p[0])
			all = append(all, p[1])
		}
	}
	if len(all) == 0 {
		return []string{r.st(r.S.Muted, "(no points)")}
	}
	if !r.Color || r.w() < 24 {
		var rows [][]string
		for _, s := range series {
			if len(s.Points) == 0 {
				continue
			}
			ys := make([]float64, len(s.Points))
			for i, p := range s.Points {
				ys[i] = p[1]
			}
			lo, hi := span(ys)
			rows = append(rows, []string{Clean(s.Name, false), Num(ys[len(ys)-1]), fmt.Sprintf("(min %s, max %s over %d)", Num(lo), Num(hi), len(ys))})
		}
		return r.cols("", rows)
	}
	x0, x1 := span(xs)
	y0, y1 := span(all)
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	y0 = math.Min(y0, 0)
	h := 8
	lc := linechart.New(r.w(), h, x0, x1, y0, y1, linechart.WithXYSteps(4, 2),
		linechart.WithStyles(r.S.Muted, r.S.Muted, lipgloss.NewStyle().Foreground(r.S.Bar)),
		linechart.WithXLabelFormatter(func(_ int, v float64) string { return Num(v) }),
		linechart.WithYLabelFormatter(func(_ int, v float64) string { return Num(v) }))
	lc.DrawXYAxisAndLabel()
	var legend []string
	for i, s := range series {
		st := r.seriesStyle(i)
		for j := 1; j < len(s.Points); j++ {
			lc.DrawBrailleLineWithStyle(canvas.Float64Point{X: s.Points[j-1][0], Y: s.Points[j-1][1]}, canvas.Float64Point{X: s.Points[j][0], Y: s.Points[j][1]}, st)
		}
		if len(s.Points) == 1 {
			lc.DrawBrailleLineWithStyle(canvas.Float64Point{X: s.Points[0][0], Y: s.Points[0][1]}, canvas.Float64Point{X: s.Points[0][0], Y: s.Points[0][1]}, st)
		}
		if len(s.Points) > 0 {
			legend = append(legend, st.Render("━")+" "+Clean(s.Name, false)+" "+r.st(r.S.Muted, Num(s.Points[len(s.Points)-1][1])))
		}
	}
	out := strings.Split(lc.View(), "\n")
	return append(out, ui.Truncate(strings.Join(legend, "   "), r.w()))
}
