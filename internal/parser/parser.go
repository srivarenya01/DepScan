// Package parser reads lock files and manifests to enumerate pinned dependencies.
package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var reSemVer = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)

// Dep is a single pinned dependency.
type Dep struct {
	Lang    string
	Name    string
	Version string
	Latest  string // populated by registry lookup
}

// ParseDeps detects and parses the appropriate lock file in repoRoot.
func ParseDeps(lang, repoRoot string) ([]Dep, error) {
	var deps []Dep
	var err error
	switch lang {
	case "python":
		deps, err = parsePython(repoRoot)
	case "node":
		deps, err = parseNode(repoRoot)
	case "go":
		deps, err = parseGo(repoRoot)
	default:
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}
	for i := range deps {
		deps[i].Lang = lang
	}
	// Filter out invalid or unpinned versions (wildcards, ranges)
	var valid []Dep
	for _, d := range deps {
		if reSemVer.MatchString(d.Version) {
			valid = append(valid, d)
		}
	}
	return valid, err
}

// ParseDir scans a repository for all supported dependency files.
func ParseDir(root string) ([]Dep, error) {
	var allDeps []Dep
	langs := []string{"python", "node", "go"}
	for _, lang := range langs {
		deps, err := ParseDeps(lang, root)
		if err == nil {
			allDeps = append(allDeps, deps...)
		}
	}
	return allDeps, nil
}

var rePipFreeze = regexp.MustCompile(`^([A-Za-z0-9_.\-]+)==([^\s]+)$`)

func parsePython(root string) ([]Dep, error) {
	// 1. requirements.txt (pip freeze format)
	if deps, err := parsePipFreeze(filepath.Join(root, "requirements.txt")); err == nil && len(deps) > 0 {
		return deps, nil
	}
	// 2. Pipfile.lock
	if deps, err := parsePipfileLock(filepath.Join(root, "Pipfile.lock")); err == nil && len(deps) > 0 {
		return deps, nil
	}
	// 3. pyproject.toml (uv/poetry lock fallback)
	if deps, err := parsePoetryLock(filepath.Join(root, "poetry.lock")); err == nil && len(deps) > 0 {
		return deps, nil
	}
	return nil, nil
}

func parsePipFreeze(path string) ([]Dep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var deps []Dep
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := rePipFreeze.FindStringSubmatch(line); m != nil {
			deps = append(deps, Dep{Name: strings.ToLower(m[1]), Version: m[2]})
		}
	}
	if err := sc.Err(); err != nil {
		return deps, err
	}
	return deps, nil
}

func parsePipfileLock(path string) ([]Dep, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var deps []Dep
	for section, pkgs := range raw {
		if section == "_meta" {
			continue
		}
		for name, rawPkg := range pkgs {
			var pkg struct {
				Version string `json:"version"`
			}
			if err := json.Unmarshal(rawPkg, &pkg); err != nil {
				continue
			}
			ver := strings.TrimPrefix(pkg.Version, "==")
			if ver != "" {
				deps = append(deps, Dep{Name: strings.ToLower(name), Version: ver})
			}
		}
	}
	return deps, nil
}

var rePoetryPkg = regexp.MustCompile(`^name\s*=\s*"([^"]+)"`)
var rePoetryVer = regexp.MustCompile(`^version\s*=\s*"([^"]+)"`)

func parsePoetryLock(path string) ([]Dep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var deps []Dep
	var cur Dep
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "[[package]]") {
			if cur.Name != "" {
				deps = append(deps, cur)
			}
			cur = Dep{}
		} else if m := rePoetryPkg.FindStringSubmatch(line); m != nil {
			cur.Name = strings.ToLower(m[1])
		} else if m := rePoetryVer.FindStringSubmatch(line); m != nil {
			cur.Version = m[1]
		}
	}
	if cur.Name != "" {
		deps = append(deps, cur)
	}
	if err := sc.Err(); err != nil {
		return deps, err
	}
	return deps, nil
}

func parseNode(root string) ([]Dep, error) {
	pjPath := filepath.Join(root, "package.json")
	if _, err := os.Stat(pjPath); os.IsNotExist(err) {
		return nil, nil // Not a Node project or missing manifest
	}

	pjData, err := os.ReadFile(pjPath)
	if err != nil {
		return nil, err
	}
	var pj struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(pjData, &pj); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool)
	for k := range pj.Dependencies {
		allowed[k] = true
	}
	for k := range pj.DevDependencies {
		allowed[k] = true
	}

	if deps, err := parsePackageLock(filepath.Join(root, "package-lock.json"), allowed); err == nil && len(deps) > 0 {
		return deps, nil
	}
	if deps, err := parseYarnLock(filepath.Join(root, "yarn.lock"), allowed); err == nil && len(deps) > 0 {
		return deps, nil
	}
	return nil, nil
}

func parsePackageLock(path string, allowed map[string]bool) ([]Dep, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var deps []Dep
	for key, pkg := range raw.Packages {
		if !strings.HasPrefix(key, "node_modules/") {
			continue
		}
		name := strings.TrimPrefix(key, "node_modules/")
		if !allowed[name] {
			continue
		}
		if pkg.Version != "" {
			deps = append(deps, Dep{Name: name, Version: pkg.Version})
		}
	}
	return deps, nil
}

var reYarnLine = regexp.MustCompile(`^"?(@?[^@"]+)@[^"]+":?$`)
var reYarnVer = regexp.MustCompile(`^\s+version "([^"]+)"`)

func parseYarnLock(path string, allowed map[string]bool) ([]Dep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var deps []Dep
	var cur string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := reYarnLine.FindStringSubmatch(line); m != nil {
			cur = strings.TrimSpace(m[1])
		} else if m := reYarnVer.FindStringSubmatch(line); m != nil && cur != "" {
			if allowed[cur] {
				deps = append(deps, Dep{Name: cur, Version: m[1]})
			}
			cur = ""
		}
	}
	return deps, nil
}

func parseGo(root string) ([]Dep, error) {
	if deps, err := parseGoMod(filepath.Join(root, "go.mod")); err == nil && len(deps) > 0 {
		return deps, nil
	}
	return nil, nil
}

func parseGoMod(path string) ([]Dep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var deps []Dep
	sc := bufio.NewScanner(f)
	inRequire := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "require (") {
			inRequire = true
			continue
		}
		if inRequire && strings.HasPrefix(line, ")") {
			inRequire = false
			continue
		}

		if inRequire || strings.HasPrefix(line, "require ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				nameIdx := 0
				if parts[0] == "require" {
					nameIdx = 1
				}
				if nameIdx+1 < len(parts) {
					// Optionally ignore indirect annotations, but for now we'll collect all explicitly required dependencies.
					if len(parts) > nameIdx+2 && parts[nameIdx+2] == "//" && len(parts) > nameIdx+3 && parts[nameIdx+3] == "indirect" {
						continue
					}
					deps = append(deps, Dep{Name: parts[nameIdx], Version: parts[nameIdx+1]})
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return deps, err
	}
	return deps, nil
}
