package llm

import (
	"fmt"
	"strings"

	"github.com/srivarenya01/DepScan/internal/extractor"
)

func BuildDiffPrompt(pkg, oldVer, newVer string, entryPoints []string, usages map[string][]string, diffs []extractor.Diff, deepScan bool) string {
	var sb strings.Builder

	sb.WriteString("You are a senior software engineer reviewing a dependency upgrade.\n\n")

	sb.WriteString("The user's codebase ONLY directly calls the following ENTRY POINT functions from this library:\n")
	sb.WriteString(strings.Join(entryPoints, ", ") + "\n\n")

	sb.WriteString("Below are EXACT source-code diffs for these entry points AND any internal functions they dynamically call under the hood that changed.\n")
	if deepScan {
		sb.WriteString("NOTE: This is a DEEP SCAN. Extra internal helper functions have been extracted for deeper context. Be aware of potential extraction noise from common methods.\n\n")
	}
	sb.WriteString("VERDICT CRITERIA:\n")
	sb.WriteString("- BREAKING: Use ONLY if you are SURE the modification behaviorally breaks the user's ENTRY POINT usage (e.g., a function now raises an error or has deleted core logic). Changing error message text ALONE is usually NOT breaking unless it affects control flow.\n")
	sb.WriteString("- SAFE: Use ONLY if you are SURE the changes do not affect the entry point's behavior for the user.\n")
	sb.WriteString("- CAUTION: Use if you are NOT SURE, do not know the implications, or if the logic changed in a way that might require user attention but doesn't obviously break everything.\n\n")
	sb.WriteString("IMPORTANT: BE ALERT for changes in raised exceptions or default argument values, as these often constitute behavioral breaks even if the code structure looks similar.\n\n")

	sb.WriteString("NOTE ON EXTRACTION NOISE: For common method names (like 'get', 'set'), ignore diffs that look like internal helpers if they don't match the entry point's purpose.\n\n")

	// Per-diff source token budget: cap individual function sources to 8000 chars
	// so that deep scans with many helpers don't explode the context window.
	const maxSourceChars = 8000
	// Total prompt token budget: ~30k tokens ≈ 120k chars. Stop adding diffs beyond this.
	const maxPromptChars = 120000

	for _, d := range diffs {
		if sb.Len() > maxPromptChars {
			sb.WriteString("\n[... additional diffs omitted: prompt budget reached. Review remaining symbols manually.]\n")
			break
		}
		callerStr := ""
		if len(d.EntryPaths) > 0 && !(len(d.EntryPaths) == 1 && d.EntryPaths[0] == d.Symbol) {
			callerStr = fmt.Sprintf(" (Called by: %s)", strings.Join(d.EntryPaths, ", "))
		}

		fmt.Fprintf(&sb, "=== %s [%s]%s ===\n", d.Symbol, pkg, callerStr)

		// Include user usage snippets if available for this symbol
		if snippets := usages[d.Symbol]; len(snippets) > 0 {
			sb.WriteString("YOUR USAGE (Call-site examples from your codebase):\n")
			for _, snip := range snippets {
				fmt.Fprintf(&sb, "  - %s\n", snip)
			}
			sb.WriteString("\n")
		}

		if !d.OldSrc.Found && !d.NewSrc.Found {
			sb.WriteString("  (source not located - function may use dynamic exports, re-exports, or bundled/minified code)\n\n")
			continue
		}
		if !d.OldSrc.Found {
			newSource := strings.ReplaceAll(d.NewSrc.Source, "\r", "")
			if len(newSource) > maxSourceChars {
				newSource = newSource[:maxSourceChars] + "\n  [... truncated]"
			}
			fmt.Fprintf(&sb, "  NEW (did not exist in Old Version):\n%s\n\n", newSource)
			continue
		}
		if !d.NewSrc.Found {
			oldSource := strings.ReplaceAll(d.OldSrc.Source, "\r", "")
			if len(oldSource) > maxSourceChars {
				oldSource = oldSource[:maxSourceChars] + "\n  [... truncated]"
			}
			fmt.Fprintf(&sb, "  REMOVED (existed in Old Version):\n%s\n\n", oldSource)
			continue
		}
		if !d.Changed {
			fmt.Fprintf(&sb, "  Identical in both versions - no change.\n\n")
			continue
		}
		sb.WriteString("```diff\n")
		// Scrub exact version numbers from diff headers too
		unified := d.Unified
		unified = strings.ReplaceAll(unified, oldVer, "Old Version")
		unified = strings.ReplaceAll(unified, newVer, "New Version")
		unified = strings.ReplaceAll(unified, "\r", "")
		if len(unified) > maxSourceChars {
			unified = unified[:maxSourceChars] + "\n[... diff truncated]"
		}
		sb.WriteString(unified)
		sb.WriteString("\n```\n\n")
	}
	sb.WriteString(responseFormat())
	return sb.String()
}

func BuildFallbackPrompt(pkg, oldVer, newVer string, symbols []string) string {
	fnList := "None (transitive or general analysis)"
	if len(symbols) > 0 {
		fnList = strings.Join(symbols, ", ")
	}
	return fmt.Sprintf(
		"You are a senior software engineer reviewing a dependency upgrade.\n\n"+
			"Library: %s  |  %s -> %s\nFunctions used: %s\n\n"+
			"Source extraction was unavailable for this dependency. Please output a generalized CAUTION warning to the user.\n\n%s",
		pkg, oldVer, newVer, fnList, responseFormat())
}

func responseFormat() string {
	return `Analyze ONLY the information above. You MUST respond in valid JSON format.
Strictly adhere to the Verdict Hierarchy:
- SAFE/BREAKING only when 100% sure.
- CAUTION when unsure or when behavioral changes are complex.

Return EXACTLY this JSON structure:
{
  "verdict": "SAFE" | "CAUTION" | "BREAKING",
  "affected_functions": ["fn1", "fn2"],
  "breaking_changes": ["desc1", "desc2"],
  "migration_notes": "steps here",
  "summary": "one line summary"
}
`
}
