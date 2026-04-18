package modelmanager

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type KnownModel struct {
	ID, HFRepo, Filename, Description string
	SizeMB, MinRAMMB                  int
}

var Registry = []KnownModel{
	{"qwen2.5-3b", "bartowski/Qwen2.5-3B-Instruct-GGUF", "Qwen2.5-3B-Instruct-Q4_K_M.gguf", "Qwen 2.5 3B - fast, great code analysis (default)", 1900, 3000},
	{"phi3.5-mini", "bartowski/Phi-3.5-mini-instruct-GGUF", "Phi-3.5-mini-instruct-Q4_K_M.gguf", "Microsoft Phi-3.5 Mini - excellent code understanding", 2200, 3500},
	{"codellama-7b", "TheBloke/CodeLlama-7B-Instruct-GGUF", "codellama-7b-instruct.Q4_K_M.gguf", "CodeLlama 7B - purpose-built for code", 4100, 6000},
}

func CacheDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".cache", "depscan", "models")
	os.MkdirAll(dir, 0755)
	return dir
}

func ModelPath(m *KnownModel) string { return filepath.Join(CacheDir(), m.Filename) }

func IsCached(m *KnownModel) bool {
	_, err := os.Stat(ModelPath(m))
	return err == nil
}

func PrintRegistry() {
	fmt.Printf("\nAvailable models (--local-model <id>):\n\n  %-20s  %-8s  %s\n", "ID", "Size", "Description")
	fmt.Printf("  %-20s  %-8s  %s\n", strings.Repeat("-", 20), "--------", strings.Repeat("-", 40))
	for _, m := range Registry {
		tag := ""
		if IsCached(&m) {
			tag = " [cached]"
		}
		fmt.Printf("  %-20s  ~%4dMB  %s%s\n", m.ID, m.SizeMB, m.Description, tag)
	}
	fmt.Println()
}

var dlClient = &http.Client{Timeout: 0}

func DownloadModel(m *KnownModel) error {
	dest := ModelPath(m)
	if IsCached(m) {
		return nil
	}
	url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", m.HFRepo, m.Filename)
	fmt.Fprintf(os.Stderr, "  Downloading %s (~%dMB)...\n", m.Filename, m.SizeMB)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "depscan/0.1.0")
	resp, err := dlClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, last: time.Now()}
	_, err = io.Copy(f, pr)
	fmt.Fprintln(os.Stderr)
	return err
}

type progressReader struct {
	r     io.Reader
	read  int64
	total int64
	last  time.Time
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.read += int64(n)
	if time.Since(p.last) > 400*time.Millisecond {
		p.last = time.Now()
		mb := float64(p.read) / 1024 / 1024
		if p.total > 0 {
			fmt.Fprintf(os.Stderr, "\r  %.1f%% (%.0f/%.0f MB)",
				float64(p.read)/float64(p.total)*100, mb, float64(p.total)/1024/1024)
		} else {
			fmt.Fprintf(os.Stderr, "\r  %.1f MB...", mb)
		}
	}
	return n, err
}
