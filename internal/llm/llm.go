// Package llm routes prompts to Gemini, OpenAI, Ollama, or a local GGUF model.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/srivarenya01/DepScan/internal/localllm"
)

var httpClient = &http.Client{Timeout: 90 * time.Second}

// Config selects and configures the LLM backend.
type Config struct {
	OpenAIKey, OpenAIModel string
	GeminiKey, GeminiModel string
	OllamaURL, OllamaModel string
	LocalModelPath         string
	LocalCfg               localllm.Config
	RetryCount             int
	RetryWait              int // in seconds
}

type Analysis struct {
	Verdict           string `json:"verdict"`
	AffectedFunctions string `json:"affected_functions"`
	BreakingChanges   string `json:"breaking_changes"`
	MigrationNotes    string `json:"migration_notes"`
	Summary           string `json:"summary"`
	Raw               string `json:"-"`
}

// Analyze dispatches to the correct backend and parses the response.
func Analyze(cfg Config, prompt string) (*Analysis, error) {
	var raw string
	var err error
	ctx := context.Background()
	switch {
	case cfg.LocalModelPath != "":
		cfg.LocalCfg.ModelPath = cfg.LocalModelPath
		raw, err = localllm.Infer(cfg.LocalCfg, prompt)
	case cfg.OllamaURL != "":
		raw, err = callOllama(ctx, cfg, prompt)
	case cfg.OpenAIKey != "":
		raw, err = callOpenAI(ctx, cfg, prompt)
	case cfg.GeminiKey != "":
		raw, err = callGemini(ctx, cfg, prompt)
	default:
		return &Analysis{Verdict: "SKIPPED", Summary: "No LLM configured"}, nil
	}
	if err != nil {
		return nil, err
	}
	return parseResponse(raw), nil
}

func Backend(cfg Config) string {
	switch {
	case cfg.LocalModelPath != "":
		return fmt.Sprintf("local(%s)", cfg.LocalModelPath)
	case cfg.OllamaURL != "":
		m := cfg.OllamaModel
		if m == "" {
			m = "llama3"
		}
		return fmt.Sprintf("ollama@%s[%s]", cfg.OllamaURL, m)
	case cfg.OpenAIKey != "":
		m := cfg.OpenAIModel
		if m == "" {
			m = "gpt-4o-mini"
		}
		return fmt.Sprintf("openai[%s]", m)
	case cfg.GeminiKey != "":
		m := cfg.GeminiModel
		if m == "" {
			m = "gemini-2.5-flash"
		}
		return "gemini@" + m
	default:
		return "none"
	}
}

func callOpenAI(ctx context.Context, cfg Config, prompt string) (string, error) {
	model := cfg.OpenAIModel
	if model == "" {
		model = "gpt-4o-mini"
	}
	body, _ := json.Marshal(map[string]interface{}{
		"model": model, "max_tokens": 2000, "temperature": 0.2,
		"messages":        []map[string]string{{"role": "user", "content": prompt}},
		"response_format": map[string]string{"type": "json_object"},
	})

	return callWithRetry(ctx, cfg, "openai", func() (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.OpenAIKey)
		return httpClient.Do(req)
	})
}

func callOllama(ctx context.Context, cfg Config, prompt string) (string, error) {
	model := cfg.OllamaModel
	if model == "" {
		model = "llama3"
	}
	url := strings.TrimRight(cfg.OllamaURL, "/") + "/api/generate"
	body, _ := json.Marshal(map[string]interface{}{
		"model": model, "prompt": prompt, "stream": false, "format": "json",
	})

	return callWithRetry(ctx, cfg, "ollama", func() (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return httpClient.Do(req)
	})
}

func callGemini(ctx context.Context, cfg Config, prompt string) (string, error) {
	model := cfg.GeminiModel
	if model == "" {
		model = "gemini-2.5-flash"
	}
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/%s:generateContent?key=%s", model, cfg.GeminiKey)

	body, _ := json.Marshal(map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"temperature": 0.2,
		},
	})

	return callWithRetry(ctx, cfg, "gemini", func() (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return httpClient.Do(req)
	})
}

func callWithRetry(ctx context.Context, cfg Config, provider string, callFn func() (*http.Response, error)) (string, error) {
	var lastErr error
	for i := 0; i <= cfg.RetryCount; i++ {
		if i > 0 {
			select {
			case <-time.After(time.Duration(cfg.RetryWait) * time.Second):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		resp, err := callFn()
		if err != nil {
			lastErr = err
			continue
		}

		if provider == "gemini" {
			var data struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			err := json.NewDecoder(resp.Body).Decode(&data)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			if data.Error != nil {
				lastErr = fmt.Errorf("%s API: %s", provider, data.Error.Message)
				if resp.StatusCode == 429 {
					continue
				}
				return "", lastErr
			}
			if len(data.Candidates) > 0 && len(data.Candidates[0].Content.Parts) > 0 {
				return data.Candidates[0].Content.Parts[0].Text, nil
			}
		} else if provider == "ollama" {
			if resp.StatusCode != 200 {
				lastErr = fmt.Errorf("ollama returned HTTP %d", resp.StatusCode)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				continue
			}
			var ol struct {
				Response string `json:"response"`
			}
			err := json.NewDecoder(resp.Body).Decode(&ol)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			return ol.Response, nil
		} else {
			// OpenAI style
			if resp.StatusCode == 429 {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				continue
			}
			var data struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			err = json.NewDecoder(resp.Body).Decode(&data)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if data.Error != nil {
				lastErr = fmt.Errorf("%s API: %s", provider, data.Error.Message)
				continue
			}
			if len(data.Choices) > 0 {
				return data.Choices[0].Message.Content, nil
			}
		}
		lastErr = fmt.Errorf("%s: unexpected empty response", provider)
	}
	return "", lastErr
}

// extractFirstJSON extracts the first balanced JSON object from a string.
// This is safer than a greedy `\{.*\}` match which would consume everything
// between the first { and the very last } in the document.
func extractFirstJSON(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' && inStr {
			esc = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func parseResponse(raw string) *Analysis {
	a := &Analysis{Verdict: "UNKNOWN", Raw: raw}

	var match string
	if m := extractFirstJSON(raw); m != "" {
		match = m
	} else {
		// No JSON braces found at all, fall back to parsing the raw text
		match = "invalid_json" // will force the Unmarshal error below
	}

	// Use a flexible map for initial unmarshal to handle both strings and arrays
	var rawMap map[string]interface{}
	if err := json.Unmarshal([]byte(match), &rawMap); err != nil {
		// FALL_BACK: If JSON is invalid, attempt to extract Verdict and Summary lines manually.
		// This handles cases where smaller models output unformatted text.
		lines := strings.Split(raw, "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			upper := strings.ToUpper(l)
			if strings.HasPrefix(upper, "VERDICT:") {
				a.Verdict = strings.TrimSpace(l[len("VERDICT:"):])
			} else if strings.HasPrefix(upper, "SUMMARY:") {
				a.Summary = strings.TrimSpace(l[len("SUMMARY:"):])
			}
		}

		if a.Verdict == "UNKNOWN" || a.Verdict == "" {
			// Super fallback: scan text for the first explicit verdict word
			uRaw := strings.ToUpper(raw)
			idxSafe := strings.Index(uRaw, "SAFE")
			if idxSafe == -1 {
				idxSafe = 999999
			}
			idxBreak := strings.Index(uRaw, "BREAKING")
			if idxBreak == -1 {
				idxBreak = 999999
			}
			idxCaution := strings.Index(uRaw, "CAUTION")
			if idxCaution == -1 {
				idxCaution = 999999
			}

			if idxSafe < idxBreak && idxSafe < idxCaution {
				a.Verdict = "SAFE"
			} else if idxBreak < idxSafe && idxBreak < idxCaution {
				a.Verdict = "BREAKING"
			} else if idxCaution < idxSafe && idxCaution < idxBreak {
				a.Verdict = "CAUTION"
			}
		}

		// If JSON parsing failed entirely, the explanations remain trapped in the raw text.
		// Dump the raw text into the BreakingChanges field to preserve the reasoning.
		if a.BreakingChanges == "" {
			a.BreakingChanges = "[Unstructured Output]\n" + strings.TrimSpace(raw)
		}
	} else {
		val := func(key string) string {
			v, ok := rawMap[key]
			if !ok {
				return ""
			}
			switch t := v.(type) {
			case string:
				return t
			case []interface{}:
				var parts []string
				for _, item := range t {
					parts = append(parts, fmt.Sprint(item))
				}
				return strings.Join(parts, "\n")
			default:
				return fmt.Sprint(v)
			}
		}

		a.Verdict = strings.ToUpper(strings.TrimSpace(val("verdict")))
		a.Summary = val("summary")
		a.MigrationNotes = val("migration_notes")
		a.BreakingChanges = val("breaking_changes")
		a.AffectedFunctions = val("affected_functions")
	}

	// Normalize verdict: default to CAUTION for any non-standard values.
	// This prevents unknown, empty, or incorrectly generated verdicts from propagating.
	switch a.Verdict {
	case "SAFE", "BREAKING", "CAUTION":
		// Valid verdict, keep unchanged.
	default:
		a.Verdict = "CAUTION"
	}

	// Final safeguard: ensure UI is not blank if a flagged verdict was assigned without explanation.
	if (a.Verdict == "BREAKING" || a.Verdict == "CAUTION") && a.BreakingChanges == "" && a.MigrationNotes == "" {
		if strings.Contains(raw, "{") {
			a.BreakingChanges = "[Warning] The analyzer assigned a verdict but failed to provide a structural explanation in its JSON response."
		} else {
			a.BreakingChanges = "[Warning] The analyzer provided unstructured text:\n" + strings.TrimSpace(raw)
		}
	}

	return a
}
