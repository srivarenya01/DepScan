# DepScan Examples

This directory contains simple demo applications to test `depscan` across multiple programming languages.

## How to test

```bash
# Run the demo
go run ./cmd/depscan --dir examples/<demo-name>

# Run the demo with verbose output
go run ./cmd/depscan --dir examples/<demo-name> -v

# Run the demo with Gemini API key
go run ./cmd/depscan --dir examples/<demo-name> --gemini-key="<your-gemini-key>"

# Run the demo with Gemini API key and model
go run ./cmd/depscan --dir examples/<demo-name> --gemini-key="<your-gemini-key>" --gemini-model="<your-gemini-model>"

# Run the demo with Ollama API key
go run ./cmd/depscan --dir examples/<demo-name> --ollama-url="<your-ollama-url>"

# Run the demo with Ollama API key and model
go run ./cmd/depscan --dir examples/<demo-name> --ollama-url="<your-ollama-url>" --ollama-model="<your-ollama-model>"

# Run the demo with OpenAI API key
go run ./cmd/depscan --dir examples/<demo-name> --openai-key="<your-openai-key>"

# Run the demo with OpenAI API key and model
go run ./cmd/depscan --dir examples/<demo-name> --openai-key="<your-openai-key>" --openai-model="<your-openai-model>"
```