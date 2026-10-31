package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
)

// State is what the agent writes to state/agent.json every couple of seconds, for `sat` (the live
// view) and `sat status` to read. It is written beside and renamed into place.
type State struct {
	Project     string    `json:"project"`
	Node        string    `json:"node"`
	Version     string    `json:"version"`
	PID         int       `json:"pid"`
	Running     bool      `json:"running"`
	Started     time.Time `json:"started"`
	Updated     time.Time `json:"updated"`
	Hub         string    `json:"hub"`
	HubOK       bool      `json:"hub_ok"`
	HubErr      string    `json:"hub_error,omitempty"`
	LastContact time.Time `json:"last_contact"`
	Protocol    string    `json:"protocol"`
	ProtocolOK  bool      `json:"protocol_ok"`
	Draining    bool      `json:"draining"`
	Note        string    `json:"note,omitempty"` // why the agent isn't claiming, when it isn't
	Lanes       []Lane    `json:"lanes"`
}

type Lane struct {
	Slot     int       `json:"slot"`
	ID       string    `json:"id,omitempty"`
	Line     string    `json:"line,omitempty"`
	Started  time.Time `json:"started"`
	Renewed  time.Time `json:"renewed"`
	Progress string    `json:"progress,omitempty"`
	Percent  int       `json:"percent"` // -1: none given
	Waiting  string    `json:"waiting,omitempty"`
}

func StatePath(p *conf.Project) string { return filepath.Join(p.State, "agent.json") }

func ReadState(p *conf.Project) (*State, error) {
	b, err := os.ReadFile(StatePath(p))
	if err != nil {
		return nil, err
	}
	var s State
	return &s, json.Unmarshal(b, &s)
}

// Live: the state is from an agent that is running now.
func (s *State) Live() bool { return s != nil && s.Running && time.Since(s.Updated) < 15*time.Second }

var pctRe = regexp.MustCompile(`^(\d{1,3})%\s*(.*)$`)

// Progress: the last line a job appended to $SAT_PROGRESS, and its leading NN% if it has one.
func Progress(path string) (string, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", -1
	}
	defer f.Close()
	fi, _ := f.Stat()
	off := max(fi.Size()-4096, 0)
	b := make([]byte, fi.Size()-off)
	f.ReadAt(b, off)
	lines := strings.Split(strings.TrimRight(string(b), "\n\r "), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if m := pctRe.FindStringSubmatch(l); m != nil {
		if n, _ := strconv.Atoi(m[1]); n <= 100 {
			return m[2], n
		}
	}
	return l, -1
}

// Tail: the last n lines of a file.
func Tail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, _ := f.Stat()
	off := max(fi.Size()-int64(n)*200-1024, 0)
	b := make([]byte, fi.Size()-off)
	f.ReadAt(b, off)
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	l := strings.Split(s, "\n")
	if off > 0 {
		l = l[1:]
	}
	return l[max(len(l)-n, 0):]
}
