// Package finding is the shared result type for lint and check, plus its output formats.
package finding

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Level is the RFC 2119 requirement level of the violated rule.
type Level string

// Requirement levels.
const (
	Must   Level = "MUST"
	Should Level = "SHOULD"
)

// Finding is a single rule violation.
type Finding struct {
	RuleID string `json:"ruleId"`
	Level  Level  `json:"level"`
	// Location is an RFC 6901 JSON Pointer with a leading '#'.
	Location string `json:"location"`
	// Line is the 1-based source line, 0 if unknown.
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
	// Subject names what was checked, e.g. "GET https://host/v1/orders"; empty for single-document runs.
	Subject string `json:"subject,omitempty"`
	// Occurrences is how many identical findings were merged by Group; 0 when ungrouped.
	Occurrences int `json:"occurrences,omitempty"`
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// Pointer appends tokens to base ("#" or an existing pointer), escaping each token.
func Pointer(base string, tokens ...string) string {
	var b strings.Builder
	b.WriteString(base)
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(pointerEscaper.Replace(t))
	}
	return b.String()
}

// Sort orders findings by Line, Subject, Location, RuleID; ties keep their input order.
func Sort(fs []Finding) {
	slices.SortStableFunc(fs, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Subject, b.Subject),
			cmp.Compare(a.Location, b.Location),
			cmp.Compare(a.RuleID, b.RuleID),
		)
	})
}

// Group merges findings that are identical apart from Line.
// The result keeps first-seen order and the first Line; Occurrences counts the merged findings.
func Group(fs []Finding) []Finding {
	type key struct {
		subject, ruleID, location, message string
		level                              Level
	}
	var out []Finding
	idx := map[key]int{}
	for _, f := range fs {
		k := key{f.Subject, f.RuleID, f.Location, f.Message, f.Level}
		if i, ok := idx[k]; ok {
			out[i].Occurrences++
			continue
		}
		f.Occurrences = 1
		idx[k] = len(out)
		out = append(out, f)
	}
	return out
}

// Failed reports whether the findings should fail a run: any MUST, or any finding when strict.
func Failed(fs []Finding, strict bool) bool {
	if strict {
		return len(fs) > 0
	}
	return slices.ContainsFunc(fs, func(f Finding) bool { return f.Level == Must })
}

// WriteText writes one line per finding plus a summary line.
func WriteText(w io.Writer, source string, fs []Finding) error {
	if len(fs) == 0 {
		_, err := fmt.Fprintf(w, "%s: no findings\n", source)
		return err
	}
	var b strings.Builder
	var musts, shoulds int
	for _, f := range fs {
		b.WriteString(source)
		if f.Line != 0 {
			fmt.Fprintf(&b, ":%d", f.Line)
		}
		fmt.Fprintf(&b, ": %s %s ", f.Level, f.RuleID)
		if f.Subject != "" {
			b.WriteString(f.Subject + " ")
		}
		fmt.Fprintf(&b, "%s: %s", f.Location, f.Message)
		if f.Occurrences > 1 {
			fmt.Fprintf(&b, " (%d entries)", f.Occurrences)
		}
		b.WriteByte('\n')
		switch f.Level {
		case Must:
			musts++
		case Should:
			shoulds++
		}
	}
	fmt.Fprintf(&b, "%d finding(s): %d MUST, %d SHOULD\n", len(fs), musts, shoulds)
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteJSON writes {"source": ..., "findings": [...]}; findings is never null.
func WriteJSON(w io.Writer, source string, fs []Finding) error {
	if fs == nil {
		fs = []Finding{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(struct {
		Source   string    `json:"source"`
		Findings []Finding `json:"findings"`
	}{source, fs})
}
