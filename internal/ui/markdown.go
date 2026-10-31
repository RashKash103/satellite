package ui

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	mdCode = regexp.MustCompile("`([^`]+)`")
	mdBold = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	mdLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdList = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
)

// Markdown renders release notes for a terminal: headings, lists with hanging indents, `code`,
// **bold** and links, wrapped to the width. Plain, it is returned as it is.
func (t *T) Markdown(md string) string {
	if !t.color {
		return md
	}
	w := t.width
	if w <= 0 || w > 88 {
		w = 88
	}
	inline := func(s string) string {
		s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
			p := mdLink.FindStringSubmatch(m)
			if p[1] == p[2] {
				return t.Accent(p[2])
			}
			return p[1] + " " + t.Muted("("+p[2]+")")
		})
		s = mdBold.ReplaceAllStringFunc(s, func(m string) string {
			p := mdBold.FindStringSubmatch(m)
			return t.Bold(p[1] + p[2])
		})
		return mdCode.ReplaceAllStringFunc(s, func(m string) string { return t.Accent(m[1 : len(m)-1]) })
	}
	wrap := func(s, first, rest string) string {
		body := lipgloss.Wrap(s, max(w-Width(first), 20), " ")
		ls := strings.Split(body, "\n")
		for i := range ls {
			if i == 0 {
				ls[i] = first + ls[i]
			} else {
				ls[i] = rest + ls[i]
			}
		}
		return strings.Join(ls, "\n")
	}
	var out []string
	fence := false
	for _, l := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		x := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(x, "```"):
			fence = !fence
		case fence:
			out = append(out, "    "+t.Accent(l))
		case strings.HasPrefix(x, "#"):
			out = append(out, t.Heading(strings.TrimSpace(strings.TrimLeft(x, "#"))))
		case mdList.MatchString(l):
			p := mdList.FindStringSubmatch(l)
			ind := "  " + strings.Repeat(" ", len(p[1]))
			mark := "•"
			if p[2][0] >= '0' && p[2][0] <= '9' {
				mark = p[2]
			}
			out = append(out, wrap(inline(p[3]), ind+t.Accent(mark)+" ", ind+strings.Repeat(" ", Width(mark)+1)))
		case x == "":
			out = append(out, "")
		default:
			out = append(out, wrap(inline(x), "", ""))
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}
