package localllm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Config struct {
	ModelPath   string
	NCtx        int
	NThreads    int
	NGPULayers  int
	Temperature float64
	MaxTokens   int
	PythonBin   string
}

func (c *Config) setDefaults() {
	if c.NCtx == 0 {
		c.NCtx = 4096
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 900
	}
	if c.Temperature == 0 {
		c.Temperature = 0.1
	}
	if c.PythonBin == "" {
		c.PythonBin = FindPython()
	}
}

func Infer(cfg Config, prompt string) (string, error) {
	cfg.setDefaults()
	if cfg.ModelPath == "" {
		return "", fmt.Errorf("model path is empty")
	}
	if _, err := os.Stat(cfg.ModelPath); err != nil {
		return "", fmt.Errorf("model not found: %s", cfg.ModelPath)
	}
	if cfg.PythonBin == "" {
		return "", fmt.Errorf("no Python 3.9+ found")
	}
	script, err := writeRunner()
	if err != nil {
		return "", fmt.Errorf("runner write failed: %w", err)
	}
	defer os.Remove(script)
	payload, _ := json.Marshal(map[string]interface{}{
		"model_path": cfg.ModelPath, "prompt": prompt,
		"n_ctx": cfg.NCtx, "n_threads": cfg.NThreads,
		"n_gpu_layers": cfg.NGPULayers,
		"temperature":  cfg.Temperature, "max_tokens": cfg.MaxTokens,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, cfg.PythonBin, script)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stderr = io.Discard // stderr is captured via the JSON error field in stdout
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("subprocess error: %w", err)
	}
	var resp struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		// Fallback: If the Python runner prints raw text instead of JSON, just return it.
		return strings.TrimSpace(string(out)), nil
	}
	if resp.Error != "" {
		return "", fmt.Errorf("llama-cpp: %s", resp.Error)
	}
	return resp.Text, nil
}

func FindPython() string {
	if p := VenvPython(); p != "" {
		return p
	}
	for _, c := range []string{"python3", "python", "python3.12", "python3.11", "python3.10", "python3.9"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func VenvPython() string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".cache", "depscan", "venv", "bin", "python3")
	if runtime.GOOS == "windows" {
		p = filepath.Join(home, ".cache", "depscan", "venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

const runnerScript = `
import sys, json, traceback
try:
    import llama_cpp
except ImportError:
    print(json.dumps({"error": "llama-cpp-python not installed"}))
    sys.exit(1)
try:
    req = json.loads(sys.stdin.read())
    llm = llama_cpp.Llama(
        model_path=req["model_path"], n_ctx=req.get("n_ctx",4096),
        n_threads=req.get("n_threads") or None,
        n_gpu_layers=req.get("n_gpu_layers",0), verbose=False)
    try:
        out = llm.create_chat_completion(
            messages=[{"role":"user","content":req["prompt"]}],
            max_tokens=req.get("max_tokens",900), temperature=req.get("temperature",0.1))
        text = out["choices"][0]["message"]["content"]
    except Exception:
        out = llm(req["prompt"], max_tokens=req.get("max_tokens",900))
        text = out["choices"][0]["text"]
    print(json.dumps({"text": text.strip()}))
except Exception as e:
    print(json.dumps({"error": str(e), "traceback": traceback.format_exc()}))
    sys.exit(1)
`

func writeRunner() (string, error) {
	f, err := os.CreateTemp("", "depscan-runner-*.py")
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.WriteString(runnerScript)
	return f.Name(), err
}
