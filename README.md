# DepScan

> DepScan is a hybrid dependency upgrade analysis tool designed to track how library updates impact your specific codebase. By combining native structural diff extraction with Large Language Model (LLM) reasoning, DepScan evaluates behavioral changes, exception semantics, and call-chain removals to provide a detailed upgrade safety verdict.

## Features
* **Call-Graph Precision**: Natively tracks which functions and internal helpers your project specifically imports and analyzes only the relevant execution paths.
* **Structural Break Detection**: Automatically flags explicitly removed identifiers in upgraded dependencies without relying on LLMs.
* **Semantic Analysis**: Analyzes changes to exception types, default parameters, and edge-case behavior to warn developers about subtle regressions.
* **Cosmetic Change Avoidance**: Filters out trivial text formatting and comment updates to reduce token usage and improve analysis speed.

## Supported Languages
* **Python**: Supports `requirements.txt`, `Pipfile`, and `poetry.lock`.
* **Node.js**: Supports `package.json`, `package-lock.json`, and `yarn.lock`.
* **Go**: Supports `go.mod` logic with AST-based function level detection mapping.

## Usage

> **Security API Key Notice:** We strongly recommend passing API keys via environment variables (`GEMINI_API_KEY`, `OPENAI_API_KEY`) rather than via CLI flags (`--gemini-key`). Using CLI flags persists your keys in process monitoring histories (e.g. `ps aux` or `/proc/pid/cmdline`), which is a security risk in shared CI/CD environments.

DepScan can be invoked from the command line on your project directory:

```bash
# Run with Gemini API (gemma-3-1b-it, gemma-3-27b-it, or gemini-2.5-flash)
./bin/depscan.exe --dir ./my-project --gemini-model="gemma-3-27b-it"

# Verbose output to see extracted symbols
./bin/depscan.exe --dir ./my-project -v --gemini-key="YOUR_API_KEY" --gemini-model="gemma-3-27b-it" 
```

### CI/CD Integration
DepScan is engineered with native hooks to drop immediately into your build pipelines.

**GitHub Actions:**
Append `--github-annotations` and/or `--pr-comment` to automatically generate inline file errors on Pull Requests and a Markdown summary table of your dependency changes.
```bash
./bin/depscan.exe --dir . --github-annotations --pr-comment --gemini-key="YOUR_API_KEY"
```

**Slack Webhooks:**
Use `--slack-webhook` to pass a Block Kit JSON payload to your Slack channel for security or CI monitoring.
```bash
./bin/depscan.exe --dir . --slack-webhook="https://hooks.slack.com/services/YOUR/JSON/WEBHOOK" --gemini-key="YOUR_API_KEY"
```


## Demos

The `examples` directory contains several small repositories designed to demonstrate different upgrade scenarios:

* `node-example2` (Express structural break)
* `python-example2` (Pydantic v2 removals and deprecations)
* `go-example1` (Safe version bumps)
* `node-example3` (jsonwebtoken security and behavioral changes)
* `python-example3` (Requests exception wrapper changes)
* `node-example1` (Safe version bumps)
* `python-example1` (Safe version bumps)

## TODO

* **Model Compatibility**: We are yet to test the analysis engine with other open-source models and API providers (e.g., OpenAI, Anthropic, local Llama instances). Currently, we have only tested and optimized the prompting and parser fallback behavior using the GEMINI API interface.
* **Extended Language Support**: Expand extraction capabilities to Rust codebases.
