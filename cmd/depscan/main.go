package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/srivarenya01/DepScan/internal/cache"
	"github.com/srivarenya01/DepScan/internal/extractor"
	"github.com/srivarenya01/DepScan/internal/llm"
	"github.com/srivarenya01/DepScan/internal/modelmanager"
	"github.com/srivarenya01/DepScan/internal/notify"
	"github.com/srivarenya01/DepScan/internal/parser"
	"github.com/srivarenya01/DepScan/internal/registry"
	"github.com/srivarenya01/DepScan/internal/reporter"
	"github.com/srivarenya01/DepScan/internal/scanner"
)

var (
	reTD    = regexp.MustCompile(`(?s)""".*?"""|'''.*?'''`)
	rePyC   = regexp.MustCompile(`(?m)#.*$`)
	reSlash = regexp.MustCompile(`(?m)//.*$`)
	reBlock = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reWS    = regexp.MustCompile(`\s+`)
)

// normalizeForCosmetic strips comments, docstrings, and normalizes quotes/whitespace
// so that cosmetic-only diffs can be detected without calling the LLM.
func normalizeForCosmetic(src string) string {
	// Strip Python triple-quoted docstrings ("""...""") and ('''...''')
	src = reTD.ReplaceAllString(src, "")
	// Strip Python single-line comments (#)
	src = rePyC.ReplaceAllString(src, "")
	// Strip JS/Go single-line comments (//)
	src = reSlash.ReplaceAllString(src, "")
	// Strip JS/Go block comments (/* ... */)
	src = reBlock.ReplaceAllString(src, "")
	// Normalize quote styles: double quotes -> single quotes
	src = strings.ReplaceAll(src, `"`, `'`)
	// Collapse all whitespace
	src = reWS.ReplaceAllString(strings.TrimSpace(src), " ")
	return src
}

// isCosmeticDiff returns true if the diff between old and new source is only
// comments, docstrings, or quote style (single vs double), with no logic changes.
func isCosmeticDiff(d extractor.Diff) bool {
	if !d.Changed {
		return true
	}
	if !d.OldSrc.Found || !d.NewSrc.Found {
		// Additions or deletions are never purely cosmetic
		return false
	}
	return normalizeForCosmetic(d.OldSrc.Source) == normalizeForCosmetic(d.NewSrc.Source)
}

func isTerminal() bool {
	fileInfo, _ := os.Stderr.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

func main() {
	dir := flag.String("dir", ".", "project root directory")
	openaiKey := flag.String("openai-key", os.Getenv("OPENAI_API_KEY"), "OpenAI API key")
	openaiModel := flag.String("openai-model", "gpt-4o-mini", "OpenAI model")
	geminiKey := flag.String("gemini-key", os.Getenv("GEMINI_API_KEY"), "Gemini API key")
	geminiModel := flag.String("gemini-model", "gemini-2.5-flash", "Gemini model")
	ollamaURL := flag.String("ollama-url", "", "Ollama base URL (e.g. http://localhost:11434)")
	ollamaModel := flag.String("ollama-model", "llama3", "Ollama model name")
	localModel := flag.String("local-model", "", "GGUF model path or known ID (use --list-models)")
	listModels := flag.Bool("list-models", false, "List available GGUF models and exit")
	downloadModel := flag.String("download-model", "", "Download a model by ID and exit")
	verbose := flag.Bool("v", false, "Verbose output (show exact extracted functions deeply traced)")
	outputJSON := flag.Bool("json", false, "Output results as JSON")
	outputMD := flag.Bool("markdown", false, "Output results as Markdown table")
	ghAnnotations := flag.Bool("github-annotations", false, "Emit GitHub Actions annotations")
	prComment := flag.Bool("pr-comment", false, "Print PR comment body to stdout")
	slackWebhook := flag.String("slack-webhook", "", "Post results to Slack webhook URL")
	useCache := flag.Bool("cache", false, "Enable LLM result caching")
	noExtract := flag.Bool("no-extract", false, "Skip source extraction (rely purely on LLM reasoning without AST diffs)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	displayPrompts := flag.Bool("display-llm-prompts", false, "Display the full LLM prompt sent for analysis")
	retryCount := flag.Int("retry-count", 0, "Number of retries for LLM calls (default 0)")
	retryWait := flag.Int("retry-wait", 30, "Seconds to wait between retries (default 30)")
	deepScan := flag.Bool("deep-scan", false, "Deeper recursive extraction of internal function calls")
	flag.Parse()

	if *versionFlag {
		fmt.Println("DepScan version 0.1.0")
		os.Exit(0)
	}

	if *deepScan {
		fmt.Fprintln(os.Stderr, "\033[33m[WARNING] DEEP SCAN ACTIVE: Results may be less accurate with smaller models. It is advised to use advanced models for deep-scan for more accurate information detailing.\033[0m")
	}

	if *listModels {
		modelmanager.PrintRegistry()
		return
	}
	if *downloadModel != "" {
		for i, m := range modelmanager.Registry {
			if m.ID == *downloadModel {
				if err := modelmanager.DownloadModel(&modelmanager.Registry[i]); err != nil {
					fmt.Fprintln(os.Stderr, "download error:", err)
					os.Exit(1)
				}
				fmt.Println("Downloaded:", m.Filename)
				return
			}
		}
		fmt.Fprintln(os.Stderr, "unknown model ID:", *downloadModel)
		os.Exit(1)
	}

	// Resolve local model ID → path
	resolvedModel := *localModel
	if resolvedModel != "" {
		for _, m := range modelmanager.Registry {
			if m.ID == resolvedModel {
				if !modelmanager.IsCached(&m) {
					fmt.Fprintf(os.Stderr, "Model %q not downloaded. Run: depscan --download-model %s\n", m.ID, m.ID)
					os.Exit(1)
				}
				resolvedModel = modelmanager.ModelPath(&m)
				break
			}
		}
	}

	llmCfg := llm.Config{
		OpenAIKey:      *openaiKey,
		OpenAIModel:    *openaiModel,
		GeminiKey:      *geminiKey,
		GeminiModel:    *geminiModel,
		OllamaURL:      *ollamaURL,
		OllamaModel:    *ollamaModel,
		LocalModelPath: resolvedModel,
		RetryCount:     *retryCount,
		RetryWait:      *retryWait,
	}

	fmt.Fprintf(os.Stderr, "depscan - scanning %s (llm: %s)\n", *dir, llm.Backend(llmCfg))

	deps, err := parser.ParseDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse error:", err)
		os.Exit(1)
	}
	if len(deps) == 0 {
		fmt.Fprintln(os.Stderr, "no pinned dependencies found")
		os.Exit(0)
	}

	usageMap := make(scanner.Usages)
	depsByLang := make(map[string][]string)
	for _, d := range deps {
		depsByLang[d.Lang] = append(depsByLang[d.Lang], d.Name)
	}
	for lang, pkgs := range depsByLang {
		langUsages := scanner.ScanRepo(lang, *dir, pkgs)
		for pkg, syms := range langUsages {
			usageMap[pkg] = syms
		}
	}

	rc := cache.New("")
	var results []reporter.Result
	hadError := false

	for _, dep := range deps {
		latest, err := registry.LatestVersion(dep.Lang, dep.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [skip] %s: registry error: %v\n", dep.Name, err)
			continue
		}
		if latest == dep.Version {
			fmt.Fprintf(os.Stderr, "  [up-to-date] %s@%s\n", dep.Name, dep.Version)
			continue
		}

		symbolMap := usageMap[dep.Name]
		var symbols []string
		for sym := range symbolMap {
			symbols = append(symbols, sym)
		}
		sort.Strings(symbols)

		if len(symbols) == 0 {
			continue // Skip transitive dependencies that are not directly used
		}

		fmt.Fprintf(os.Stderr, "  [analyze] %s  %s → %s  (%d symbols)\n",
			dep.Name, dep.Version, latest, len(symbols))

		// Mix in usage snippets to the cache key to ensure context-sensitive cache hits
		var usageContent []string
		for _, sym := range symbols {
			usageContent = append(usageContent, strings.Join(symbolMap[sym], "|"))
		}
		cacheKey := cache.Key(dep.Name, dep.Version, latest, symbols, llm.Backend(llmCfg), usageContent)

		if *useCache {
			if cached, ok := rc.Get(cacheKey); ok {
				fmt.Fprintf(os.Stderr, "    (cached)\n")
				results = append(results, cached)
				continue
			}
		}

		var diffs []extractor.Diff
		var allExplored bool
		var analysis *llm.Analysis

		if !*noExtract && (dep.Lang == "python" || dep.Lang == "node" || dep.Lang == "go") && len(symbols) > 0 {
			allExplored, diffs, err = extractor.ExtractDiffs(dep.Lang, dep.Name, dep.Version, latest, symbols, *deepScan)
			if err != nil {
				fmt.Fprintf(os.Stderr, "    [warn] extraction failed: %v - using fallback\n", err)
			}
		}

		var prompt string
		if len(diffs) > 0 {
			if *verbose {
				fmt.Fprintf(os.Stderr, "    [verbose] extracted %d raw functions natively:\n", len(diffs))
				for _, d := range diffs {
					pStr := ""
					if len(d.EntryPaths) > 0 && !(len(d.EntryPaths) == 1 && d.EntryPaths[0] == d.Symbol) {
						pStr = fmt.Sprintf(" [%s]", strings.Join(d.EntryPaths, ", "))
					} else if len(d.EntryPaths) == 1 && d.EntryPaths[0] == d.Symbol {
						pStr = " [main]"
					}
					fmt.Fprintf(os.Stderr, "      - %s%s\n", d.Symbol, pStr)
				}
			}
			prompt = llm.BuildDiffPrompt(dep.Name, dep.Version, latest, symbols, symbolMap, diffs, *deepScan)
		} else {
			prompt = llm.BuildFallbackPrompt(dep.Name, dep.Version, latest, symbols)
		}

		if *displayPrompts {
			fmt.Fprintf(os.Stderr, "\n=== LLM PROMPT (%s) ===\n%s\n=== END PROMPT ===\n\n", dep.Name, prompt)
		}
		var missingEntryPoints []string
		isEntryPoint := make(map[string]bool)
		for _, s := range symbols {
			isEntryPoint[s] = true
		}

		// Track cosmetic-only status.
		// allCosmetic is true only when ALL diffs (entry points AND helpers) are cosmetic.
		allCosmetic := true
		var removedHelpers []string

		for _, d := range diffs {
			// Any non-cosmetic change - whether to an entry point or a helper - makes this non-trivial
			if d.Changed && !isCosmeticDiff(d) {
				allCosmetic = false
			}

			if d.OldSrc.Found && !d.NewSrc.Found {
				if isEntryPoint[d.Symbol] {
					missingEntryPoints = append(missingEntryPoints, d.Symbol)
				} else {
					for _, ep := range d.EntryPaths {
						if isEntryPoint[ep] {
							removedHelpers = append(removedHelpers, d.Symbol)
							break
						}
					}
				}
			}
		}

		if len(missingEntryPoints) > 0 || len(removedHelpers) > 0 {
			// Structural break detected. Continue to invoke the analyzer to capture behavioral changes (e.g., deprecations)
			// and merge the results for a comprehensive report.
			var structuralMsg string
			if len(missingEntryPoints) > 0 {
				structuralMsg = fmt.Sprintf("Entry point(s) no longer available: %s.", strings.Join(missingEntryPoints, ", "))
			}
			if len(removedHelpers) > 0 {
				if structuralMsg != "" {
					structuralMsg += " "
				}
				structuralMsg += fmt.Sprintf("Internal helper(s) removed from call chain of your entry points: %s.", strings.Join(removedHelpers, ", "))
			}

			llmAnalysis, llmErr := llm.Analyze(llmCfg, prompt)
			if llmErr != nil {
				fmt.Fprintf(os.Stderr, "    [warn] llm error: %v\n", llmErr)
				hadError = true
			}

			// Build a clean, deduplicated breaking changes list.
			// Start with the structural findings as the first bullet.
			var breakingLines []string
			breakingLines = append(breakingLines, structuralMsg)

			if llmAnalysis != nil && llmAnalysis.BreakingChanges != "" {
				// Incorporate analyzer findings line-by-line, skipping empty or duplicate entries.
				for _, line := range strings.Split(strings.ReplaceAll(llmAnalysis.BreakingChanges, "\r", ""), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					// Skip if this finding is already captured in the structural message.
					// Use normalized full-line comparison - not a 40-char prefix which could drop distinct entries.
					normLine := strings.ToLower(strings.TrimLeft(line, "-*• "))
					if strings.Contains(strings.ToLower(structuralMsg), normLine) {
						continue
					}
					breakingLines = append(breakingLines, line)
				}
			}

			// Deduplicate the list itself
			seen := make(map[string]bool)
			var uniqueLines []string
			for _, l := range breakingLines {
				norm := strings.ToLower(strings.TrimSpace(l))
				if norm != "" && !seen[norm] {
					seen[norm] = true
					uniqueLines = append(uniqueLines, l)
				}
			}

			allRemoved := append(missingEntryPoints, removedHelpers...)
			summary := fmt.Sprintf("Structural break: removed %s", strings.Join(allRemoved, ", "))
			if llmAnalysis != nil && llmAnalysis.Summary != "" {
				cleanSum := strings.TrimSpace(strings.ReplaceAll(llmAnalysis.Summary, "\r", ""))
				if cleanSum != "" {
					summary += ". " + cleanSum
				}
			}
			migrationNotes := "Review the library's official migration guide for renamed/removed APIs."
			if llmAnalysis != nil && llmAnalysis.MigrationNotes != "" {
				cleanMig := strings.TrimSpace(strings.ReplaceAll(llmAnalysis.MigrationNotes, "\r", ""))
				if cleanMig != "" {
					migrationNotes = cleanMig
				}
			}

			analysis = &llm.Analysis{
				Verdict:         "BREAKING",
				BreakingChanges: strings.Join(uniqueLines, "\n"),
				MigrationNotes:  migrationNotes,
				Summary:         summary,
			}
		} else if allCosmetic && len(diffs) > 0 && allExplored {
			// Only skip the LLM when all extracted code is identical AND
			// we explored the entire reachable call chain (allExplored).
			if *verbose {
				fmt.Fprintf(os.Stderr, "    [verbose] skipped llm: all %d extracted functions are cosmetically identical\n", len(diffs))
			}
			analysis = &llm.Analysis{
				Verdict: "SAFE",
				Summary: "Cosmetic changes only (comments/docstrings/formatting)",
			}
		} else {
			analysis, err = llm.Analyze(llmCfg, prompt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "    [warn] llm error: %v\n", err)
				hadError = true
			}
		}

		r := reporter.Result{
			Package:       dep.Name,
			Current:       dep.Version,
			Latest:        latest,
			UsedFunctions: symbols,
			Usages:        symbolMap,
			Diffs:         diffs,
			Analysis:      analysis,
		}
		if *useCache {
			if err := rc.Set(cacheKey, r); err != nil {
				fmt.Fprintf(os.Stderr, "    [warn] cache write error: %v\n", err)
			}
		}
		results = append(results, r)
	}

	switch {
	case *outputJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results)
	case *outputMD:
		reporter.PrintMarkdown(results)
	default:
		// In terminal mode route to stderr so verbose output (already on stderr)
		// and the table are on the same stream - prevents interleaving.
		reporter.PrintTable(results, isTerminal(), os.Stderr)
	}

	if *ghAnnotations {
		notify.GithubAnnotations(results)
	}
	if *prComment {
		notify.PRComment(results)
	}
	if *slackWebhook != "" {
		if err := notify.Slack(*slackWebhook, results); err != nil {
			fmt.Fprintln(os.Stderr, "slack notify error:", err)
		}
	}

	hasBreaking := false
	for _, r := range results {
		if r.Analysis != nil && strings.ToUpper(r.Analysis.Verdict) == "BREAKING" {
			hasBreaking = true
			break
		}
	}
	if hasBreaking {
		os.Exit(2)
	}
	if hadError {
		os.Exit(1)
	}
}

