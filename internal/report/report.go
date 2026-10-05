// Package report renders findings as text, a PR-comment markdown body, or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dbguard/dbguard/internal/rules"
)

// Marker lets the GitHub Action find and update its own PR comment.
const Marker = "<!-- dbguard-report -->"

// File groups the findings (and override problems) of one migration file.
type File struct {
	Path     string          `json:"path"`
	Findings []rules.Finding `json:"findings"`
	Problems []string        `json:"problems,omitempty"`
}

// Blocking reports whether a finding should fail the check.
func Blocking(f rules.Finding, failOn rules.Risk) bool {
	return f.Override == "" && f.Risk >= failOn
}

func Text(w io.Writer, files []File, failOn rules.Risk) {
	n := 0
	for _, fl := range files {
		for _, f := range fl.Findings {
			tag := strings.ToUpper(f.Risk.String())
			if f.Override != "" {
				tag = "ACKNOWLEDGED"
			}
			fmt.Fprintf(w, "%s:%d: [%s] %s (%s)\n    %s\n    safer: %s\n", fl.Path, f.Line, tag, f.Table, f.Rule, f.Message, f.Alternative)
			if f.Override != "" {
				fmt.Fprintf(w, "    override reason: %s\n", f.Override)
			}
			if Blocking(f, failOn) {
				n++
			}
		}
		for _, p := range fl.Problems {
			fmt.Fprintf(w, "%s: warning: %s\n", fl.Path, p)
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "dbguard: no blocking migration risks found")
	} else {
		fmt.Fprintf(w, "dbguard: %d blocking finding(s)\n", n)
	}
}

func Markdown(w io.Writer, files []File, failOn rules.Risk) {
	fmt.Fprintln(w, Marker)
	blocking := 0
	var body strings.Builder
	for _, fl := range files {
		for _, f := range fl.Findings {
			status := "**" + strings.ToUpper(f.Risk.String()) + "**"
			if f.Override != "" {
				status = "acknowledged"
			} else if Blocking(f, failOn) {
				blocking++
			}
			fmt.Fprintf(&body, "### `%s` - %s\n`%s` line %d (rule `%s`)\n\n", f.Table, status, fl.Path, f.Line, f.Rule)
			fmt.Fprintf(&body, "- **Lock:** %s\n- **Impact:** %s\n", f.Lock, f.Message)
			if f.Estimate != nil {
				fmt.Fprintf(&body, "- **Estimated lock:** %s (an estimate, not a promise)\n", f.Estimate)
			}
			fmt.Fprintf(&body, "- **Safer alternative:** %s\n", f.Alternative)
			if f.Override != "" {
				fmt.Fprintf(&body, "- **Override reason:** %s\n", f.Override)
			} else {
				fmt.Fprintf(&body, "- To accept this risk, add above the statement: `-- dbguard:ignore %s reason: <why>`\n", f.Rule)
			}
			body.WriteString("\n")
		}
		for _, p := range fl.Problems {
			fmt.Fprintf(&body, "> warning (`%s`): %s\n\n", fl.Path, p)
		}
	}
	if blocking == 0 {
		fmt.Fprintln(w, "## DB Guard: no blocking migration risks")
	} else {
		fmt.Fprintf(w, "## DB Guard: %d risky migration change(s) block this merge\n", blocking)
	}
	fmt.Fprint(w, "\n", body.String())
}

func JSON(w io.Writer, files []File) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(files)
}
