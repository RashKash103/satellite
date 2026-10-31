package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/RashKash103/satellite/internal/conf"
)

// HistCap: the values kept per metric; HistNames: the metrics kept per history.
const (
	HistCap   = 120
	HistNames = 32
)

// Hist: the recent values of a view's metric blocks, by name, oldest first. The live view keeps
// one per view in memory; the command line keeps one in a small file (HistFile).
type Hist struct {
	mu sync.Mutex
	M  map[string][]float64 `json:"metrics"`
	// Seq: the order names were last pushed, for dropping the stalest past HistNames.
	Seq map[string]int `json:"seq"`
	N   int            `json:"n"`
}

func NewHist() *Hist { return &Hist{M: map[string][]float64{}, Seq: map[string]int{}} }

// Push adds v to name's values, keeping the last HistCap.
func (h *Hist) Push(name string, v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.M == nil {
		h.M, h.Seq = map[string][]float64{}, map[string]int{}
	}
	s := append(h.M[name], v)
	if len(s) > HistCap {
		s = append([]float64(nil), s[len(s)-HistCap:]...)
	}
	h.M[name] = s
	h.N++
	h.Seq[name] = h.N
	if len(h.M) > HistNames {
		names := make([]string, 0, len(h.M))
		for n := range h.M {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool { return h.Seq[names[i]] < h.Seq[names[j]] })
		for _, n := range names[:len(names)-HistNames] {
			delete(h.M, n)
			delete(h.Seq, n)
		}
	}
}

// Get: name's values, oldest first.
func (h *Hist) Get(name string) []float64 {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]float64(nil), h.M[name]...)
}

// Record pushes each metric block of d.
func (h *Hist) Record(d *Doc) {
	if h == nil || d == nil {
		return
	}
	for _, b := range d.Blocks {
		if b.Type == "metric" && b.Value != nil {
			h.Push(b.Name, *b.Value)
		}
	}
}

// HistFile: where the command line keeps a view's metric history.
func HistFile(p *conf.Project, name string) string {
	return filepath.Join(p.State, "views", name+".metrics.json")
}

// LoadHist reads a history file (missing or unreadable: empty).
func LoadHist(path string) *Hist {
	h := NewHist()
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, h)
	}
	if h.M == nil {
		h.M = map[string][]float64{}
	}
	if h.Seq == nil {
		h.Seq = map[string]int{}
	}
	return h
}

// Save writes the history (renamed into place).
func (h *Hist) Save(path string) error {
	h.mu.Lock()
	b, err := json.Marshal(h)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	return conf.WriteAtomic(path, b, 0o644)
}
