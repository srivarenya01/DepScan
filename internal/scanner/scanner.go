// Package scanner walks repository source files and detects every symbol
// (function, class, constant) used from each tracked package.
package scanner

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Usages maps package name -> symbol name -> list of unique call-site snippets found in the repo.
type Usages map[string]map[string][]string

// ScanRepo walks repoRoot and returns all detected usages per package.
func ScanRepo(lang, repoRoot string, packages []string) Usages {
	usages := make(Usages)
	for _, p := range packages {
		usages[p] = make(map[string][]string)
	}
	const maxSymbols = 30
	const branchLimit = 5
	// In deep-scan mode:
	//   - Global cap: 100 symbols total
	//   - Branching factor: up to 10 discovered sub-calls per function, recursively
	// In standard mode:
	//   - Global cap: 30 symbols total
	//   - Branching factor: up to 5 discovered sub-calls per function (shallow)
	//   - Go language: no recursion at all (flat entry-point only)
	switch lang {
	case "python":
		scanPython(repoRoot, packages, usages)
	case "node":
		scanJS(repoRoot, packages, usages)
	case "go":
		scanGo(repoRoot, packages, usages)
	}
	for pkg, fns := range usages {
		if len(fns) == 0 {
			delete(usages, pkg)
		}
	}
	return usages
}

func recordUsage(usages Usages, pkg, sym, line string) {
	if usages[pkg] == nil {
		usages[pkg] = make(map[string][]string)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	// Cap unique snippets to keep prompts reasonable
	const maxSnippets = 5
	for _, existing := range usages[pkg][sym] {
		if existing == line {
			return
		}
	}
	if len(usages[pkg][sym]) < maxSnippets {
		usages[pkg][sym] = append(usages[pkg][sym], line)
	}
}

var (
	rePyImport     = regexp.MustCompile(`^import\s+(\S+)(?:\s+as\s+(\S+))?`)
	rePyFromImport = regexp.MustCompile(`^from\s+(\S+)\s+import\s+(.+)`)
	rePyCall       = regexp.MustCompile(`\b(\w+)\.(\w+)\s*\(`)
	rePyAsgn       = regexp.MustCompile(`\b(\w+)\s*=\s*([\w.]+)\s*\(.*\)`)
	rePyClass      = regexp.MustCompile(`class\s+(\w+)\s*\(([^)]+)\)`)
)

func scanPython(repoRoot string, packages []string, usages Usages) {
	pkgSet := toSet(packages)

	_ = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(path, ".py") {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		aliases := make(map[string]string)
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			originalLine := sc.Text()
			line := strings.TrimSpace(originalLine)
			if m := rePyImport.FindStringSubmatch(line); m != nil {
				root := strings.SplitN(m[1], ".", 2)[0]
				if _, ok := pkgSet[root]; ok {
					alias := m[2]
					if alias == "" {
						alias = root
					}
					aliases[alias] = root
				}
				continue
			}
			if m := rePyFromImport.FindStringSubmatch(line); m != nil {
				root := strings.SplitN(m[1], ".", 2)[0]
				if _, ok := pkgSet[root]; ok {
					for _, raw := range strings.Split(m[2], ",") {
						parts := strings.Fields(strings.TrimSpace(raw))
						if len(parts) == 0 || parts[0] == "*" {
							continue
						}
						recordUsage(usages, root, parts[0], originalLine)
						if len(parts) == 3 && parts[1] == "as" {
							aliases[parts[2]] = root
						} else {
							aliases[parts[0]] = root
						}
					}
				}
				continue
			}
			if m := rePyClass.FindStringSubmatch(line); m != nil {
				newClass, bases := m[1], m[2]
				for _, b := range strings.Split(bases, ",") {
					b = strings.TrimSpace(b)
					if pkg, ok := aliases[b]; ok {
						aliases[newClass] = pkg
					} else if _, ok := pkgSet[b]; ok {
						aliases[newClass] = b
					}
				}
				continue
			}
			if m := rePyAsgn.FindStringSubmatch(line); m != nil {
				localVar, rhs := m[1], m[2]
				root := strings.SplitN(rhs, ".", 2)[0]
				if pkg, ok := aliases[root]; ok {
					aliases[localVar] = pkg
				} else if _, ok := pkgSet[root]; ok {
					aliases[localVar] = root
				}
			}
			for _, m := range rePyCall.FindAllStringSubmatch(line, -1) {
				obj, method := m[1], m[2]
				if pkg, ok := aliases[obj]; ok {
					recordUsage(usages, pkg, method, originalLine)
				} else if _, ok := pkgSet[obj]; ok {
					recordUsage(usages, obj, method, originalLine)
				}
			}
		}
		f.Close()
		return nil
	})
}

var (
	reJSDefault    = regexp.MustCompile(`import\s+(\w+)\s+from\s+['"]([^'"]+)['"]`)
	reJSNamed      = regexp.MustCompile(`import\s+\{([^}]+)\}\s+from\s+['"]([^'"]+)['"]`)
	reJSRequire    = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	reJSMember     = regexp.MustCompile(`\b(\w+)\.(\w+)\s*\(`)
	reJSCallAsgn   = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*(\w+)\s*\(.*\)`)
	reJSCallDirect = regexp.MustCompile(`\b(\w+)\s*\(`)
	reJSSplit      = regexp.MustCompile(`[\s,]+`)
	jsExts         = map[string]bool{".js": true, ".ts": true, ".jsx": true, ".tsx": true, ".mjs": true, ".cjs": true}
)

func jsRoot(pkg string) string {
	if strings.HasPrefix(pkg, "@") {
		p := strings.SplitN(pkg, "/", 3)
		if len(p) >= 2 {
			return p[0] + "/" + p[1]
		}
	}
	return strings.SplitN(pkg, "/", 2)[0]
}

func scanJS(repoRoot string, packages []string, usages Usages) {
	pkgSet := toSet(packages)

	_ = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip node_modules at directory level
		if info.IsDir() && strings.Contains(path, "node_modules") {
			return filepath.SkipDir
		}
		if info.IsDir() || !jsExts[filepath.Ext(path)] {
			return nil
		}
		aliases := make(map[string]string)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		src := string(data)
		lines := strings.Split(src, "\n")
		// Helper to find the line content for a given regex match offset
		findLine := func(offset int) string {
			count := 0
			for _, l := range lines {
				count += len(l) + 1
				if count > offset {
					return l
				}
			}
			return ""
		}

		for _, m := range reJSDefault.FindAllStringIndex(src, -1) {
			match := reJSDefault.FindStringSubmatch(src[m[0]:m[1]])
			if len(match) < 3 {
				continue
			}
			local, pkg := match[1], jsRoot(match[2])
			if _, ok := pkgSet[pkg]; ok {
				aliases[local] = pkg
			}
		}
		for _, m := range reJSNamed.FindAllStringIndex(src, -1) {
			match := reJSNamed.FindStringSubmatch(src[m[0]:m[1]])
			if len(match) < 3 {
				continue
			}
			pkg := jsRoot(match[2])
			if _, ok := pkgSet[pkg]; ok {
				for _, raw := range reJSSplit.Split(match[1], -1) {
					raw = strings.TrimSpace(raw)
					if raw == "" || raw == "as" {
						continue
					}
					sym := strings.Fields(raw)[0]
					recordUsage(usages, pkg, sym, findLine(m[0]))
				}
			}
		}
		for _, m := range reJSRequire.FindAllStringIndex(src, -1) {
			match := reJSRequire.FindStringSubmatch(src[m[0]:m[1]])
			if len(match) < 3 {
				continue
			}
			local, pkg := match[1], jsRoot(match[2])
			if _, ok := pkgSet[pkg]; ok {
				aliases[local] = pkg
			}
		}
		for _, m := range reJSCallAsgn.FindAllStringIndex(src, -1) {
			match := reJSCallAsgn.FindStringSubmatch(src[m[0]:m[1]])
			if len(match) < 3 {
				continue
			}
			local, constructor := match[1], match[2]
			if pkg, ok := aliases[constructor]; ok {
				aliases[local] = pkg
				recordUsage(usages, pkg, "(default-call)", findLine(m[0]))
			} else if _, ok := pkgSet[constructor]; ok {
				aliases[local] = constructor
				recordUsage(usages, constructor, "(default-call)", findLine(m[0]))
			}
		}
		for _, m := range reJSCallDirect.FindAllStringIndex(src, -1) {
			match := reJSCallDirect.FindStringSubmatch(src[m[0]:m[1]])
			if len(match) < 2 {
				continue
			}
			funcName := match[1]
			if pkg, ok := aliases[funcName]; ok {
				recordUsage(usages, pkg, "(default-call)", findLine(m[0]))
			} else if _, ok := pkgSet[funcName]; ok {
				recordUsage(usages, funcName, "(default-call)", findLine(m[0]))
			}
		}
		for _, m := range reJSMember.FindAllStringIndex(src, -1) {
			match := reJSMember.FindStringSubmatch(src[m[0]:m[1]])
			if len(match) < 3 {
				continue
			}
			obj, method := match[1], match[2]
			if pkg, ok := aliases[obj]; ok {
				recordUsage(usages, pkg, method, findLine(m[0]))
			} else if _, ok := pkgSet[obj]; ok {
				recordUsage(usages, obj, method, findLine(m[0]))
			}
		}
		return nil
	})
}

func toSet(items []string) map[string]struct{} {
	s := make(map[string]struct{}, len(items))
	for _, v := range items {
		s[v] = struct{}{}
	}
	return s
}

func scanGo(repoRoot string, packages []string, usages Usages) {
	fset := token.NewFileSet()
	pkgSet := toSet(packages)

	_ = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Only skip vendor directories, not files with 'vendor' in their name
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

		aliases := make(map[string]string)
		for _, imp := range node.Imports {
			if imp.Path == nil {
				continue
			}
			impPath := strings.Trim(imp.Path.Value, `"`)
			if _, ok := pkgSet[impPath]; ok {
				var alias string
				if imp.Name != nil {
					alias = imp.Name.Name
				} else {
					parts := strings.Split(impPath, "/")
					alias = parts[len(parts)-1]
				}
				if alias != "_" && alias != "." {
					aliases[alias] = impPath
				}
			}
		}

		if len(aliases) == 0 {
			return nil
		}

		// Read source once per file for snippet extraction
		srcData, _ := os.ReadFile(path)
		lines := strings.Split(string(srcData), "\n")

		ast.Inspect(node, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					if pkgPath, ok := aliases[id.Name]; ok {
						lineIdx := fset.Position(sel.Pos()).Line - 1
						if lineIdx >= 0 && lineIdx < len(lines) {
							recordUsage(usages, pkgPath, sel.Sel.Name, lines[lineIdx])
						}
					}
				}
			}
			return true
		})

		return nil
	})
}
