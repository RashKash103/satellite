package view

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Doc: what a json view prints for one run (a stream view: one per line, each replacing the
// last). sat draws the blocks in its own style, or as plain text where there is no colour.
type Doc struct {
	Blocks []Block `json:"blocks"`
}

// Block: one part of a Doc; Type says which fields count.
//
//	text      lines (or text)
//	kv        rows of [key, value]                       (title)
//	table     columns, rows                              (title)
//	progress  label, value 0..1, note
//	bar       data [{label, value}]: horizontal bars     (title)
//	timeline  items [{label, start, end, state}]         (title)  start, end: epoch seconds; state done|running|failed|waiting
//	metric    name, value, unit: sat keeps the recent values and draws them
//	line      series [{name, points [[x, y]...]}]        (title)
//	alert     level warn|error, text
type Block struct {
	Type    string   `json:"type"`
	Title   string   `json:"title,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	Text    string   `json:"text,omitempty"`
	Rows    [][]any  `json:"rows,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Label   string   `json:"label,omitempty"`
	Value   *float64 `json:"value,omitempty"`
	Note    string   `json:"note,omitempty"`
	Data    []Datum  `json:"data,omitempty"`
	Items   []Item   `json:"items,omitempty"`
	Name    string   `json:"name,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Series  []Series `json:"series,omitempty"`
	Level   string   `json:"level,omitempty"`
}

type Datum struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type Item struct {
	Label string  `json:"label"`
	Start float64 `json:"start"`
	End   float64 `json:"end,omitempty"` // 0: still going
	State string  `json:"state,omitempty"`
}

type Series struct {
	Name   string       `json:"name"`
	Points [][2]float64 `json:"points"`
}

// Limits on what one document may hold, so a runaway view can't swamp the screen or memory.
const (
	maxBlocks = 200
	maxRows   = 2000
	maxPoints = 10000
)

var states = map[string]bool{"done": true, "running": true, "failed": true, "waiting": true, "": true}

// Parse reads one document. Any problem is an error to show in place of the view; a block of a
// type this sat doesn't know is kept, and shown as such.
func Parse(b []byte) (*Doc, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, errors.New("the view printed nothing")
	}
	var d Doc
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("not a JSON document: %v", err)
	}
	if dec.More() {
		return nil, errors.New("more than one JSON document: print one per run")
	}
	if d.Blocks == nil {
		return nil, errors.New(`no "blocks" in the document`)
	}
	if len(d.Blocks) > maxBlocks {
		d.Blocks = d.Blocks[:maxBlocks]
	}
	for i := range d.Blocks {
		bl := &d.Blocks[i]
		where := fmt.Sprintf("block %d (%s)", i+1, bl.Type)
		switch bl.Type {
		case "progress", "metric":
			if bl.Value == nil || math.IsNaN(*bl.Value) || math.IsInf(*bl.Value, 0) {
				return nil, fmt.Errorf("%s: needs a numeric value", where)
			}
			if bl.Type == "metric" && bl.Name == "" {
				return nil, fmt.Errorf("%s: needs a name", where)
			}
		case "alert":
			if bl.Level != "warn" && bl.Level != "error" {
				return nil, fmt.Errorf("%s: level %q: want warn or error", where, bl.Level)
			}
		case "timeline":
			for _, it := range bl.Items {
				if !states[it.State] {
					return nil, fmt.Errorf("%s: state %q: want done, running, failed or waiting", where, it.State)
				}
			}
		case "":
			return nil, fmt.Errorf("block %d has no type", i+1)
		}
		if len(bl.Rows) > maxRows {
			bl.Rows = bl.Rows[:maxRows]
		}
		if len(bl.Lines) > maxRows {
			bl.Lines = bl.Lines[len(bl.Lines)-maxRows:]
		}
		for j := range bl.Series {
			if p := bl.Series[j].Points; len(p) > maxPoints {
				bl.Series[j].Points = p[len(p)-maxPoints:]
			}
		}
	}
	return &d, nil
}

// Alerts: the document's alert blocks.
func (d *Doc) Alerts() []Block {
	if d == nil {
		return nil
	}
	var a []Block
	for _, b := range d.Blocks {
		if b.Type == "alert" {
			a = append(a, b)
		}
	}
	return a
}

// Num: a number as people write it: 3, 0.25, 1200000.
func Num(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// cell: a table or kv value as text.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return Num(x)
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
