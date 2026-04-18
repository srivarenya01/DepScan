package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/srivarenya01/DepScan/internal/reporter"
)

type Cache struct{ dir string }

func New(dir string) *Cache {
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache", "depscan", "results")
	}
	_ = os.MkdirAll(dir, 0755)
	return &Cache{dir: dir}
}

func Key(pkg, oldVer, newVer string, symbols []string, backend string, usageSnippets []string) string {
	sorted := append([]string{}, symbols...)
	sort.Strings(sorted)
	sortedUsage := append([]string{}, usageSnippets...)
	sort.Strings(sortedUsage)
	// Strip URLs/ports from backend (e.g., ollama@http://... -> ollama)
	if idx := strings.Index(backend, "@"); idx != -1 {
		backend = backend[:idx]
	}
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%s", pkg, oldVer, newVer, strings.Join(sorted, ","), backend, strings.Join(sortedUsage, ";"))
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func (c *Cache) Get(key string) (reporter.Result, bool) {
	data, err := os.ReadFile(filepath.Join(c.dir, key+".json"))
	if err != nil {
		return reporter.Result{}, false
	}
	var r reporter.Result
	if err := json.Unmarshal(data, &r); err != nil {
		return reporter.Result{}, false
	}
	return r, true
}

func (c *Cache) Set(key string, r reporter.Result) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.dir, key+".json"), data, 0644)
}
