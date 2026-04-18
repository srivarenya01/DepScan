// Package registry queries PyPI and npm for the
// latest published version of a package.
package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 15 * time.Second}

// LatestVersion returns the latest stable version string.
func LatestVersion(lang, pkg string) (string, error) {
	switch lang {
	case "python":
		return pypiLatest(pkg)
	case "node":
		return npmLatest(pkg)
	case "go":
		return goLatest(pkg)
	default:
		return "", fmt.Errorf("unsupported language: %s", lang)
	}
}

func pypiLatest(pkg string) (string, error) {
	resp, err := client.Get("https://pypi.org/pypi/" + pkg + "/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned HTTP %d for %s", resp.StatusCode, pkg)
	}
	var data struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.Info.Version, nil
}

func npmLatest(pkg string) (string, error) {
	resp, err := client.Get("https://registry.npmjs.org/" + pkg + "/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned HTTP %d for %s", resp.StatusCode, pkg)
	}
	var data struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.Version, nil
}

// goEscapePath escapes uppercase letters in a module path for the Go module
// proxy, which requires uppercase letters to be encoded as !lowercase.
// See https://pkg.go.dev/golang.org/x/mod/module#EscapePath
func goEscapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + 32) // to lowercase
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func goLatest(pkg string) (string, error) {
	escaped := goEscapePath(pkg)
	resp, err := client.Get(fmt.Sprintf("https://proxy.golang.org/%s/@latest", escaped))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned HTTP %d for %s", resp.StatusCode, pkg)
	}
	var data struct {
		Version string `json:"Version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	return data.Version, nil
}
