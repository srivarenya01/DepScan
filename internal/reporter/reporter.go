// Package reporter formats analysis results for the terminal, markdown, and JSON.
package reporter

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/srivarenya01/DepScan/internal/extractor"
	"github.com/srivarenya01/DepScan/internal/llm"
)

// Result is the complete analysis output for one package upgrade.
type Result struct {
	Package       string              `json:"package"`
	Current       string              `json:"current"`
	Latest        string              `json:"latest"`
	UsedFunctions []string            `json:"used_functions"`   // List of symbol names
	Usages        map[string][]string `json:"usages,omitempty"` // Symbol -> Call snippets
	Diffs         []extractor.Diff    `json:"diffs,omitempty"`
	Analysis      *llm.Analysis       `json:"analysis,omitempty"`
}

// ANSI colour codes
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	red    = "\033[31m"
	yellow = "\033[33m"
	green  = "\033[32m"
	cyan   = "\033[36m"
	gray   = "\033[90m"
)

// visLen returns the visible (non-ANSI) length of s.
func visLen(s string) int {
	inEsc := false
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if s[i] == 'm' {
				inEsc = false
			}
			continue
		}
		n++
	}
	return n
}

// padRight pads s to visible width w with spaces.
func padRight(s string, w int) string {
	v := visLen(s)
	if v >= w {
		return s
	}
	return s + strings.Repeat(" ", w-v)
}

// trunc truncates s to max visible chars, appending "..." if needed.
func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// wrapText wraps a long string to lines of at most width chars, indented by indent.
func wrapText(s, indent string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	cur := indent
	for _, w := range words {
		if len(cur)+len(w)+1 > width && cur != indent {
			lines = append(lines, cur)
			cur = indent + w
		} else if cur == indent {
			cur += w
		} else {
			cur += " " + w
		}
	}
	if cur != indent {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}

// PrintTable prints a colour table to w (use os.Stderr for terminal, os.Stdout for piping).
func PrintTable(results []Result, colour bool, w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "No packages to report on.")
		return
	}
	c := func(s, code string) string {
		if colour {
			return code + s + reset
		}
		return s
	}
	fmt.Fprintln(w)
	// Header
	fmt.Fprintf(w, "  %-30s  %-12s  %-12s  %-10s  %s\n",
		"PACKAGE", "CURRENT", "LATEST", "VERDICT", "SUMMARY")
	fmt.Fprintln(w, "  "+strings.Repeat("-", 90))

	for _, r := range results {
		verdict := "SKIPPED"
		summary := ""
		if r.Analysis != nil {
			verdict = r.Analysis.Verdict
			summary = r.Analysis.Summary
		}
		if len(summary) > 52 {
			summary = summary[:49] + "..."
		}
		var col string
		switch strings.ToUpper(verdict) {
		case "BREAKING":
			col = red
		case "CAUTION":
			col = yellow
		case "SAFE":
			col = green
		default:
			col = gray
		}
		pkgName := trunc(r.Package, 28)
		curVer := trunc(r.Current, 10)
		latVer := trunc(r.Latest, 10)

		// Pad verdict using visible width so ANSI codes don’t drift the column
		verdictPadded := padRight(c(trunc(verdict, 10), col+bold), 10)
		fmt.Fprintf(w, "  %-30s  %-12s  %-12s  %s  %s\n",
			pkgName, curVer, latVer,
			verdictPadded,
			c(summary, gray))
	}
	fmt.Fprintln(w)

	// Detail block for non-safe results
	for _, r := range results {
		if r.Analysis == nil {
			continue
		}
		v := strings.ToUpper(r.Analysis.Verdict)
		if v != "BREAKING" && v != "CAUTION" {
			continue
		}
		fmt.Fprintf(w, "  %s%s%s  %s -> %s\n", bold, r.Package, reset, r.Current, r.Latest)
		if r.Analysis.BreakingChanges != "" {
			fmt.Fprintf(w, "  %sBreaking changes:%s\n", bold, reset)
			for _, l := range strings.Split(r.Analysis.BreakingChanges, "\n") {
				l = strings.TrimSpace(l)
				if l != "" {
					fmt.Fprintln(w, wrapText("    "+l, "      ", 100))
				}
			}
		}
		if r.Analysis.MigrationNotes != "" && r.Analysis.MigrationNotes != "No changes needed" {
			fmt.Fprintf(w, "  %sMigration:%s\n", bold, reset)
			for _, l := range strings.Split(r.Analysis.MigrationNotes, "\n") {
				l = strings.TrimSpace(l)
				if l != "" {
					fmt.Fprintln(w, wrapText("    "+l, "      ", 100))
				}
			}
		}
		fmt.Fprintln(w)
	}
}

// PrintMarkdown prints a GitHub-flavoured markdown report.
func PrintMarkdown(results []Result) {
	fmt.Println("## Dependency Upgrade Safety Report")
	fmt.Println()
	fmt.Println("| Package | Current | Latest | Verdict | Summary |")
	fmt.Println("|---------|---------|--------|---------|---------|")
	for _, r := range results {
		verdict, summary := "SKIPPED", ""
		if r.Analysis != nil {
			verdict = r.Analysis.Verdict
			summary = r.Analysis.Summary
		}
		fmt.Printf("| `%s` | `%s` | `%s` | **%s** | %s |\n",
			r.Package, r.Current, r.Latest, verdict, summary)
	}
	fmt.Println()
	for _, r := range results {
		if r.Analysis == nil {
			continue
		}
		v := strings.ToUpper(r.Analysis.Verdict)
		if v != "BREAKING" && v != "CAUTION" {
			continue
		}
		fmt.Printf("### `%s` (%s -> %s)\n\n", r.Package, r.Current, r.Latest)
		if r.Analysis.AffectedFunctions != "" {
			fmt.Printf("**Affected functions:** `%s`\n\n", r.Analysis.AffectedFunctions)
		}
		if r.Analysis.BreakingChanges != "" {
			fmt.Printf("**Breaking changes:**\n\n%s\n\n", r.Analysis.BreakingChanges)
		}
		if r.Analysis.MigrationNotes != "" {
			fmt.Printf("**Migration notes:**\n\n%s\n\n", r.Analysis.MigrationNotes)
		}
	}
}
