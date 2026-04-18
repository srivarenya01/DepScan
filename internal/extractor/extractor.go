// Package extractor downloads both versions of a library, unpacks the source,
// finds each used symbol's implementation, and builds a unified diff.
package extractor

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var dlClient = &http.Client{Timeout: 5 * time.Minute}

// FunctionSource holds extracted source for one symbol.
type FunctionSource struct {
	Symbol     string
	Source     string
	Found      bool
	File       string // Absolute path
	RelFile    string // Relative to root
	Line       int
	Score      int
	InnerCalls []string // AST-extracted callee names (Go only)
}

// Diff holds old + new source and the unified diff for one symbol.
type Diff struct {
	Symbol     string
	OldSrc     FunctionSource
	NewSrc     FunctionSource
	Changed    bool
	Unified    string
	EntryPaths []string
}

// ExtractDiffs downloads both versions and returns per-symbol diffs.
// It returns (allExplored bool, diffs []Diff, err error).
func ExtractDiffs(lang, pkg, oldVer, newVer string, symbols []string, deepScan bool) (bool, []Diff, error) {
	base := filepath.Join(os.TempDir(), "depscan", pkg)
	oldDir := filepath.Join(base, oldVer)
	newDir := filepath.Join(base, newVer)
	os.MkdirAll(oldDir, 0755)
	os.MkdirAll(newDir, 0755)

	if err := downloadVersion(lang, pkg, oldVer, oldDir); err != nil {
		return false, nil, fmt.Errorf("download %s@%s: %w", pkg, oldVer, err)
	}
	if err := downloadVersion(lang, pkg, newVer, newDir); err != nil {
		return false, nil, fmt.Errorf("download %s@%s: %w", pkg, newVer, err)
	}

	var diffs []Diff
	processed := make(map[string]bool)
	initialSyms := make(map[string]bool)
	paths := make(map[string]map[string]bool)

	// depth tracks which BFS level each symbol is at.
	// Entry points are level 0; each recursive discovery increases by 1.
	type queueItem struct {
		sym   string
		depth int
	}
	queue := make([]queueItem, 0, len(symbols)*2)
	for _, s := range symbols {
		initialSyms[s] = true
		paths[s] = map[string]bool{s: true}
		queue = append(queue, queueItem{s, 0})
	}

	// In deep-scan mode:
	//   - Global cap: 100 symbols total
	//   - Branching factor: up to 10 discovered sub-calls per function, recursively
	// In standard mode:
	//   - Global cap: 30 symbols total
	//   - Branching factor: up to 5 discovered sub-calls per function (shallow)
	//   - Go language: no recursion at all (flat entry-point only)
	maxSyms := 30
	branchFactor := 5
	if deepScan {
		maxSyms = 100
		branchFactor = 10
	}

	for i := 0; i < len(queue) && i < maxSyms; i++ {
		item := queue[i]
		sym, depth := item.sym, item.depth
		if processed[sym] {
			continue
		}
		// Skip private helpers (starting with _) unless explicitly requested
		if strings.HasPrefix(sym, "_") && !initialSyms[sym] {
			continue
		}
		processed[sym] = true

		oldSrc := extractSymbol(lang, oldDir, sym, "")
		newSrc := extractSymbol(lang, newDir, sym, oldSrc.RelFile)

		if !oldSrc.Found && !newSrc.Found {
			if initialSyms[sym] {
				diffs = append(diffs, Diff{Symbol: sym})
			}
			continue
		}

		// Recurse into inner calls.
		// Go deepScan: use AST-precise call list from FunctionSource.InnerCalls.
		// Python/Node: use regex-based findInnerCalls.
		// Recursion stops when the queue reaches maxSyms.
		var inner []string
		if lang == "go" && deepScan {
			// Merge AST calls from both old and new versions
			seen := make(map[string]bool)
			for _, c := range append(oldSrc.InnerCalls, newSrc.InnerCalls...) {
				if !seen[c] && !skipIdents[c] {
					seen[c] = true
					inner = append(inner, c)
				}
			}
		} else if lang == "python" || lang == "node" {
			inner = findInnerCalls(oldSrc.Source + "\n" + newSrc.Source)
		}
		if len(inner) > 0 {
			added := 0
			for _, call := range inner {
				if added >= branchFactor {
					break
				}
				if strings.HasPrefix(call, "_") && !initialSyms[call] {
					continue
				}
				if paths[call] == nil {
					paths[call] = make(map[string]bool)
				}
				for p := range paths[sym] {
					paths[call][p] = true
				}
				if !processed[call] && len(queue) < maxSyms {
					queue = append(queue, queueItem{call, depth + 1})
					added++
				}
			}
		}

		changed := oldSrc.Source != newSrc.Source
		unified := ""
		if changed {
			unified = buildUnifiedDiff(sym, oldVer, newVer, oldSrc.Source, newSrc.Source)
		}

		var symPaths []string
		for p := range paths[sym] {
			symPaths = append(symPaths, p)
		}
		sort.Strings(symPaths)

		diffs = append(diffs, Diff{
			Symbol:     sym,
			OldSrc:     oldSrc,
			NewSrc:     newSrc,
			Changed:    changed,
			Unified:    unified,
			EntryPaths: symPaths,
		})
	}
	allExplored := len(queue) < maxSyms
	return allExplored, diffs, nil
}

var reInnerCall = regexp.MustCompile(`\b([a-zA-Z_]\w*)\s*\(`)

var skipIdents = func() map[string]bool {
	words := []string{
		// JS keywords / flow control
		"if", "for", "while", "switch", "catch", "function", "return",
		"typeof", "instanceof", "new", "delete", "void", "throw",
		"case", "else", "in", "of", "do", "with", "try", "finally",
		// JS globals
		"require", "console", "parseInt", "parseFloat", "isNaN", "isFinite",
		"encodeURIComponent", "decodeURIComponent", "encodeURI", "decodeURI",
		"setTimeout", "setInterval", "clearTimeout", "clearInterval",
		// JS built-in constructors
		"Object", "Array", "String", "Number", "Boolean", "Symbol",
		"Promise", "Error", "Map", "Set", "WeakMap", "WeakSet",
		"JSON", "Math", "Date", "RegExp", "Buffer", "process",
		"module", "exports", "global", "undefined", "null", "super",
		// JS common methods
		"stringify", "parse", "toString", "valueOf", "hasOwnProperty",
		"apply", "call", "bind", "constructor", "then", "resolve", "reject",
		"push", "pop", "shift", "unshift", "splice", "slice", "join",
		"indexOf", "includes", "find", "findIndex", "some", "every",
		"reduce", "forEach", "keys", "values", "entries", "assign",
		// Python keywords / flow control
		"def", "class", "lambda", "yield", "async", "await",
		"pass", "raise", "global", "nonlocal",
		// Python builtins
		"print", "len", "range", "str", "int", "float", "bool",
		"list", "dict", "set", "tuple", "type", "isinstance", "issubclass",
		"getattr", "setattr", "hasattr", "delattr", "open", "enumerate",
		"zip", "map", "filter", "sorted", "reversed", "repr", "format",
		"sum", "min", "max", "abs", "round", "pow", "divmod",
		"id", "hash", "hex", "oct", "bin", "ord", "chr",
		"vars", "dir", "callable", "iter", "next",
		"super", "staticmethod", "classmethod", "property",
		// Go builtins
		"make", "new", "append", "copy", "close", "panic", "recover",
		"Errorf", "Sprintf", "Printf", "Fprintf", "Println", "Fprintln",
		"Fatal", "Fatalf", "Log", "Logf",
	}
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}()

func findInnerCalls(source string) []string {
	var calls []string
	matches := reInnerCall.FindAllStringSubmatch(source, -1)
	seen := make(map[string]bool)
	for _, match := range matches {
		name := match[1]
		if seen[name] || skipIdents[name] {
			continue
		}
		seen[name] = true
		calls = append(calls, name)
	}
	return calls
}

func downloadVersion(lang, pkg, ver, destDir string) error {
	switch lang {
	case "python":
		return downloadPyPI(pkg, ver, destDir)
	case "node":
		return downloadNPM(pkg, ver, destDir)
	case "go":
		return downloadGo(pkg, ver, destDir)
	default:
		return fmt.Errorf("source extraction not yet supported for %s", lang)
	}
}

func alreadyUnpacked(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".depscan_unpacked"))
	return err == nil
}

func markUnpacked(dir string) {
	os.WriteFile(filepath.Join(dir, ".depscan_unpacked"), []byte("1"), 0644)
}

var reSafePkgNum = regexp.MustCompile(`^[A-Za-z0-9.+_-]+$`)

func downloadPyPI(pkg, ver, destDir string) error {
	if alreadyUnpacked(destDir) {
		return nil
	}
	if !reSafePkgNum.MatchString(pkg) || !reSafePkgNum.MatchString(ver) {
		return fmt.Errorf("invalid package or version name format: %s %s", pkg, ver)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pip", "download", pkg+"=="+ver, "--no-deps", "-d", destDir)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := cmd.Run(); err != nil {
		if err2 := downloadPyPIDirect(pkg, ver, destDir); err2 != nil {
			return fmt.Errorf("pip failed (%v): %s; direct also failed: %v", err, strings.TrimSpace(stderr.String()), err2)
		}
	}
	if err := unpackPyPI(destDir); err != nil {
		return err
	}
	markUnpacked(destDir)
	return nil
}

func downloadPyPIDirect(pkg, ver, destDir string) error {
	resp, err := dlClient.Get("https://pypi.org/pypi/" + pkg + "/" + ver + "/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("PyPI returned status %d for %s@%s", resp.StatusCode, pkg, ver)
	}
	var data struct {
		URLs []struct {
			URL         string `json:"url"`
			Filename    string `json:"filename"`
			PackageType string `json:"packagetype"`
		} `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	for _, u := range data.URLs {
		if u.PackageType == "bdist_wheel" || u.PackageType == "sdist" {
			return downloadFile(u.URL, filepath.Join(destDir, u.Filename))
		}
	}
	if len(data.URLs) > 0 {
		return downloadFile(data.URLs[0].URL, filepath.Join(destDir, data.URLs[0].Filename))
	}
	return fmt.Errorf("no URLs for %s==%s", pkg, ver)
}

func unpackPyPI(destDir string) error {
	entries, _ := os.ReadDir(destDir)
	for _, e := range entries {
		p := filepath.Join(destDir, e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".whl"):
			return extractZip(p, destDir)
		case strings.HasSuffix(e.Name(), ".tar.gz"):
			return extractTarGz(p, destDir)
		}
	}
	return nil
}

func downloadNPM(pkg, ver, destDir string) error {
	if alreadyUnpacked(destDir) {
		return nil
	}
	resp, err := dlClient.Get("https://registry.npmjs.org/" + pkg + "/" + ver)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("npm registry returned HTTP %d for %s@%s", resp.StatusCode, pkg, ver)
	}
	var npmData struct {
		Dist struct {
			Tarball string `json:"tarball"`
		} `json:"dist"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&npmData); err != nil {
		return err
	}
	if npmData.Dist.Tarball == "" {
		return fmt.Errorf("no tarball for %s@%s", pkg, ver)
	}
	archive := filepath.Join(destDir, "pkg.tgz")
	if err := downloadFile(npmData.Dist.Tarball, archive); err != nil {
		return err
	}
	if err := extractTarGz(archive, destDir); err != nil {
		return err
	}
	markUnpacked(destDir)
	return nil
}

func downloadGo(pkg, ver, destDir string) error {
	if alreadyUnpacked(destDir) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "mod", "download", "-json", pkg+"@"+ver)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go mod download failed (%v): %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return fmt.Errorf("go mod download: no output for %s@%s (module may not exist or proxy unavailable)", pkg, ver)
	}
	var meta struct {
		Dir string `json:"Dir"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &meta); err != nil {
		return fmt.Errorf("go mod download: failed to parse output for %s@%s: %v", pkg, ver, err)
	}
	if err := copyDir(meta.Dir, destDir); err != nil {
		return err
	}
	markUnpacked(destDir)
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		outPath := filepath.Join(dst, rel)
		if info.IsDir() {
			os.MkdirAll(outPath, 0755)
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return nil
		}
		out, err := os.Create(outPath)
		if err != nil {
			in.Close()
			return nil
		}
		_, cpErr := io.Copy(out, in)
		out.Close()
		in.Close()
		if cpErr != nil {
			os.Remove(outPath) // Remove partial file
		}
		return nil
	})
}

func extractSymbol(lang, rootDir, symbol, preferredRelPath string) FunctionSource {
	switch lang {
	case "python":
		return extractPython(rootDir, symbol, preferredRelPath)
	case "node":
		return extractJS(rootDir, symbol, preferredRelPath)
	case "go":
		return extractGo(rootDir, symbol, preferredRelPath)
	default:
		return FunctionSource{Symbol: symbol}
	}
}

var rePyDef = regexp.MustCompile(`^([ \t]*)(?:@[\w.]+(?:\([^)]*\))?\s*)*([ \t]*)(def|class)\s+(\w+)\s*[\(:]`)

func calculateScore(relPath, symbol string, sourceLen int, indent int) int {
	score := sourceLen / 10
	base := strings.ToLower(filepath.Base(relPath))
	dir := strings.ToLower(filepath.Dir(relPath))

	// Major boost for top-level functions (low indentation)
	if indent == 0 {
		score += 2000
	} else {
		score -= indent * 100
	}

	if strings.Contains(dir, "lib") || strings.Contains(dir, "src") || dir == "." {
		score += 1000
	}

	mainFiles := []string{"api.py", "index.js", "main.js", "application.js", "request.js", "response.js", "express.js", "app.js", "__init__.py", "main.py", "base.py", "models.py"}
	for _, f := range mainFiles {
		if base == f {
			score += 5000
			break
		}
	}

	if strings.Contains(dir, "node_modules") || strings.Contains(dir, "test") || strings.Contains(dir, "example") {
		score -= 2000
	}
	depth := strings.Count(relPath, string(filepath.Separator))
	score -= depth * 100

	return score
}

func extractPython(rootDir, symbol, preferredRelPath string) FunctionSource {
	if preferredRelPath != "" {
		p := preferredRelPath
		for {
			target := filepath.Join(rootDir, p)
			if f := extractPythonFile(target, rootDir, symbol); f.Found {
				return f
			}
			idx := strings.Index(p, string(filepath.Separator))
			if idx == -1 {
				break
			}
			p = p[idx+1:]
		}
	}

	var best FunctionSource
	_ = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".py") {
			return nil
		}
		f := extractPythonFile(path, rootDir, symbol)
		if !best.Found || f.Score > best.Score {
			best = f
		}
		return nil
	})

	return best
}

func extractPythonFile(path, rootDir, symbol string) FunctionSource {
	data, err := os.ReadFile(path)
	if err != nil {
		return FunctionSource{}
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		m := rePyDef.FindStringSubmatch(line)
		if m == nil || m[4] != symbol {
			continue
		}
		indent := len(m[1]) + len(m[2]) // total indentation before 'def'
		block := []string{line}
		for j := i + 1; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "" {
				block = append(block, l)
				continue
			}
			curr := len(l) - len(strings.TrimLeft(l, " \t"))
			if curr <= indent {
				break
			}
			block = append(block, l)
		}
		for len(block) > 0 && strings.TrimSpace(block[len(block)-1]) == "" {
			block = block[:len(block)-1]
		}
		if len(block) > 120 {
			block = append(block[:120], "    # [... truncated due to length ...]")
		}
		src := strings.Join(block, "\n")
		rel, _ := filepath.Rel(rootDir, path)
		score := calculateScore(rel, symbol, len(src), indent)
		return FunctionSource{Symbol: symbol, Source: src, Found: true, File: path, RelFile: rel, Line: i + 1, Score: score}
	}
	return FunctionSource{}
}

func extractJS(rootDir, symbol, preferredRelPath string) FunctionSource {
	// Build patterns once - reused across both preferredRelPath fast-path and the full Walk
	pats := buildJSPatterns(symbol)

	if preferredRelPath != "" {
		p := preferredRelPath
		for {
			target := filepath.Join(rootDir, p)
			if f := extractJSFileWithPats(target, rootDir, symbol, pats); f.Found {
				return f
			}
			idx := strings.Index(p, string(filepath.Separator))
			if idx == -1 {
				break
			}
			p = p[idx+1:]
		}
	}

	var best FunctionSource
	exts := map[string]bool{".js": true, ".ts": true, ".jsx": true, ".tsx": true, ".mjs": true, ".cjs": true}
	_ = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !exts[filepath.Ext(path)] {
			return nil
		}
		f := extractJSFileWithPats(path, rootDir, symbol, pats)
		if !best.Found || f.Score > best.Score {
			best = f
		}
		return nil
	})

	return best
}

// jsPatterns holds the pre-compiled regexes for a single JS symbol.
// Patterns are tried in descending specificity order; the one with the best
// score (longer body in a file named after the symbol) wins.
type jsPatterns struct {
	fnPat         *regexp.Regexp // function sym(...) { or const sym = function(...) {
	assignPat     *regexp.Regexp // obj.sym = function(...) {
	exportsSymPat *regexp.Regexp // exports.sym = function(...) {  (CommonJS named export)
	objPropPat    *regexp.Regexp // sym: function(...) {          (object literal method)
	objArrowPat   *regexp.Regexp // sym: (...) => {               (object literal arrow)
	moduleExpPat  *regexp.Regexp // module.exports = function(...) { (default export, file named after sym)
	aliasPat      *regexp.Regexp // .sym = anything;              (one-liner re-export, lowest priority)
}

func buildJSPatterns(symbol string) jsPatterns {
	q := regexp.QuoteMeta(symbol)
	return jsPatterns{
		fnPat:         regexp.MustCompile(`(?:function\s+` + q + `|(?:const|let|var)\s+` + q + `\s*=\s*(?:async\s+)?(?:function|\())[^{]*\{`),
		assignPat:     regexp.MustCompile(`\.` + q + `\s*=\s*(?:async\s+)?(?:function|deprecate(?:\.function)?)[\s\(\{]`),
		exportsSymPat: regexp.MustCompile(`exports\.` + q + `\s*=\s*(?:async\s+)?function`),
		objPropPat:    regexp.MustCompile(`(?:^|[,{\s])` + q + `\s*:\s*(?:async\s+)?function`),
		objArrowPat:   regexp.MustCompile(`(?:^|[,{\s])` + q + `\s*:\s*(?:async\s+)?\([^)]*\)\s*=>`),
		moduleExpPat:  regexp.MustCompile(`module\.exports\s*=\s*(?:async\s+)?function`),
		aliasPat:      regexp.MustCompile(`\.` + q + `\s*=\s*[^;\n]+;`),
	}
}

func extractJSFileWithPats(path, rootDir, symbol string, pats jsPatterns) FunctionSource {
	data, err := os.ReadFile(path)
	if err != nil {
		return FunctionSource{}
	}
	src := string(data)

	var best FunctionSource
	// tryPat attempts to extract a function body using pat.
	// Unlike a short-circuit approach, every pattern is tried so the
	// highest-scoring match (e.g. the actual implementation file vs
	// a re-export one-liner) always wins.
	tryPat := func(pat *regexp.Regexp, oneliner bool) {
		loc := pat.FindStringIndex(src)
		if loc == nil {
			return
		}
		start := loc[0]
		var extracted string
		if oneliner {
			end := start
			for end < len(src) && src[end] != '\n' {
				end++
			}
			extracted = src[start:end]
		} else {
			// Walk brace-by-brace, skipping strings and template literals to avoid false depth changes
			depth, end := 0, start
			inStr := false
			strChar := byte(0)
			inTemplate := false
			for i := start; i < len(src); i++ {
				c := src[i]
				if inStr {
					if c == '\\' {
						i++ // skip escaped char
						continue
					}
					if c == strChar {
						inStr = false
					}
					continue
				}
				if inTemplate {
					if c == '\\' {
						i++
						continue
					}
					if c == '`' {
						inTemplate = false
					}
					continue
				}
				if c == '\'' || c == '"' {
					inStr = true
					strChar = c
					continue
				}
				if c == '`' {
					inTemplate = true
					continue
				}
				if c == '{' {
					depth++
				} else if c == '}' {
					depth--
					if depth == 0 {
						end = i + 1
						break
					}
				}
			}
			if end == start {
				end = loc[1] + 80
				if end > len(src) {
					end = len(src)
				}
			}
			extracted = src[start:end]
		}
		lines := strings.Split(extracted, "\n")
		if len(lines) > 80 {
			lines = lines[:80]
		}
		extracted = strings.Join(lines, "\n")
		rel, _ := filepath.Rel(rootDir, path)
		score := calculateScore(rel, symbol, len(extracted), 0) // JS indentation detection is more complex, set to 0 for now
		// Always compare scores - don't short-circuit. A module.exports function
		// body in verify.js will outscore a one-liner re-export in index.js.
		if !best.Found || score > best.Score {
			lineNum := strings.Count(src[:start], "\n") + 1
			best = FunctionSource{Symbol: symbol, Source: extracted, Found: true, File: path, RelFile: rel, Line: lineNum, Score: score}
		}
	}

	// Try patterns in descending specificity. All are tried; highest score wins.
	tryPat(pats.fnPat, false)         // named function / const assignment
	tryPat(pats.assignPat, false)     // obj.sym = function
	tryPat(pats.exportsSymPat, false) // exports.sym = function  (CommonJS named export)
	tryPat(pats.objPropPat, false)    // sym: function  (object literal method)
	tryPat(pats.objArrowPat, false)   // sym: () =>  (object literal arrow)
	tryPat(pats.moduleExpPat, false)  // module.exports = function  (file-level default export)
	tryPat(pats.aliasPat, true)       // .sym = anything;  (one-liner re-export, lowest priority)

	return best
}

func extractGo(rootDir, symbol, preferredRelPath string) FunctionSource {
	fset := token.NewFileSet()
	var best FunctionSource

	_ = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && strings.Contains(path, "vendor") {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil
		}

		ast.Inspect(node, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			match := false
			if fd.Name.Name == symbol {
				match = true
			} else if fd.Recv != nil && len(fd.Recv.List) > 0 {
				// Handle (p *Parser) Parse() where symbol is Parser.Parse or just Parse
				// We match symbols like 'Client.Get' or '(Client).Get' or just 'Get'
				recv := ""
				switch t := fd.Recv.List[0].Type.(type) {
				case *ast.Ident:
					recv = t.Name
				case *ast.StarExpr:
					if id, ok := t.X.(*ast.Ident); ok {
						recv = id.Name
					}
				}
				if recv != "" && (recv+"."+fd.Name.Name == symbol || fd.Name.Name == symbol) {
					match = true
				}
			}
			if !match {
				return true
			}
			src, _ := os.ReadFile(path)
			start := fset.Position(fd.Pos()).Offset
			end := fset.Position(fd.End()).Offset
			if start < 0 || end > len(src) {
				return false
			}
			extracted := string(src[start:end])
			if fd.Doc != nil {
				ds := fset.Position(fd.Doc.Pos()).Offset
				if ds >= 0 {
					extracted = string(src[ds:start]) + extracted
				}
			}

			// Collect all function calls within this declaration via AST.
			// This is far more accurate than regex for Go - it ignores
			// string literals, comments, and type names entirely.
			seen := make(map[string]bool)
			var innerCalls []string
			ast.Inspect(fd.Body, func(inner ast.Node) bool {
				ce, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fn := ce.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name != "" && name != symbol && !skipIdents[name] && !seen[name] {
					seen[name] = true
					innerCalls = append(innerCalls, name)
				}
				return true
			})

			rel, _ := filepath.Rel(rootDir, path)
			lineNum := fset.Position(fd.Pos()).Line
			score := calculateScore(rel, symbol, len(extracted), 0) // Go functions are mostly top-level or method receiver (0 indent)
			if !best.Found || score > best.Score {
				best = FunctionSource{
					Symbol:     symbol,
					Source:     extracted,
					Found:      true,
					File:       path,
					RelFile:    rel,
					Line:       lineNum,
					Score:      score,
					InnerCalls: innerCalls,
				}
			}
			return false
		})
		return nil
	})
	return best
}

func buildUnifiedDiff(symbol, oldVer, newVer, oldSrc, newSrc string) string {
	if oldSrc == "" && newSrc != "" {
		return fmt.Sprintf("--- %s\t(%s)\n+++ %s\t(%s)\n@@ NEW @@\n", symbol, oldVer, symbol, newVer) +
			prefixLines("+", newSrc)
	}
	if newSrc == "" && oldSrc != "" {
		return fmt.Sprintf("--- %s\t(%s)\n+++ %s\t(%s)\n@@ REMOVED @@\n", symbol, oldVer, symbol, newVer) +
			prefixLines("-", oldSrc)
	}
	oldLines := strings.Split(oldSrc, "\n")
	newLines := strings.Split(newSrc, "\n")
	lcs := lcsLines(oldLines, newLines)
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\t(%s)\n+++ %s\t(%s)\n@@ -1,%d +1,%d @@\n",
		symbol, oldVer, symbol, newVer, len(oldLines), len(newLines))
	oi, ni, li := 0, 0, 0
	for oi < len(oldLines) || ni < len(newLines) {
		if li < len(lcs) && oi < len(oldLines) && ni < len(newLines) &&
			oldLines[oi] == lcs[li] && newLines[ni] == lcs[li] {
			sb.WriteString(" " + oldLines[oi] + "\n")
			oi++
			ni++
			li++
		} else if ni < len(newLines) && (li >= len(lcs) || newLines[ni] != lcs[li]) {
			sb.WriteString("+" + newLines[ni] + "\n")
			ni++
		} else {
			sb.WriteString("-" + oldLines[oi] + "\n")
			oi++
		}
	}
	return sb.String()
}

func prefixLines(prefix, src string) string {
	var sb strings.Builder
	for _, l := range strings.Split(src, "\n") {
		sb.WriteString(prefix + l + "\n")
	}
	return sb.String()
}

func lcsLines(a, b []string) []string {
	m, n := len(a), len(b)
	if m > 1000 || n > 1000 {
		return nil // Avoid quadratic blowup on very large files
	}
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = 1 + dp[i+1][j+1]
			} else if dp[i+1][j] > dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var res []string
	i, j := 0, 0
	for i < m && j < n {
		if a[i] == b[j] {
			res = append(res, a[i])
			i++
			j++
		} else if dp[i+1][j] > dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return res
}

func downloadFile(url, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	resp, err := dlClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		path := filepath.Join(dest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(path)+string(os.PathSeparator), filepath.Clean(dest)+string(os.PathSeparator)) {
			continue // Skip Zip Slip vulnerability
		}
		os.MkdirAll(filepath.Dir(path), 0755)
		out, err := os.Create(path)
		if err != nil {
			continue
		}
		rc, opErr := f.Open()
		if opErr != nil {
			out.Close()
			continue
		}
		// Limit decompression to 100MB per file to avoid zip bombs
		_, cpErr := io.Copy(out, io.LimitReader(rc, 100*1024*1024))
		rc.Close()
		out.Close()
		if cpErr != nil {
			os.Remove(path)
		}
	}
	return nil
}

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.SplitN(filepath.ToSlash(hdr.Name), "/", 2)
		rel := hdr.Name
		if len(parts) == 2 {
			rel = parts[1]
		}
		path := filepath.Join(dest, filepath.FromSlash(rel))
		if !strings.HasPrefix(filepath.Clean(path)+string(os.PathSeparator), filepath.Clean(dest)+string(os.PathSeparator)) {
			continue // Skip Zip Slip vulnerability
		}
		os.MkdirAll(filepath.Dir(path), 0755)
		out, err := os.Create(path)
		if err != nil {
			continue
		}
		// Limit decompression to 100MB per file to avoid zip bombs
		_, cpErr := io.Copy(out, io.LimitReader(tr, 100*1024*1024))
		out.Close()
		if cpErr != nil {
			os.Remove(path)
		}
	}
	return nil
}
