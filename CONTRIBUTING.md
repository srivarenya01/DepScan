# Contributing to DepScan

First off, thank you for considering contributing to DepScan! This tool is currently a **Work In Progress**, and community contributions are highly valued as we stabilize the engine and add support for new languages.

## How Can I Contribute?

### Reporting Bugs
If you find a bug, please create an issue containing:
* A clear and descriptive title.
* The exact command you ran and the model you used.
* The unexpected output or stack trace.
* The expected output.

*(Note: For security vulnerabilities, please refer to `SECURITY.md` and report them privately rather than opening a public issue.)*

### Suggesting Enhancements
Enhancements to the LLM prompt, the AST native extraction logic, or new language support (Go, Rust, etc.) are welcome. Please open an issue outlining the proposed design before submitting massive pull requests.

### Pull Requests
1. Fork the repo and create your branch from `main`.
2. Ensure your changes compile via `go build`.
3. Test your changes against the provided `examples/` repositories.
4. Ensure your commits are descriptive.
5. Create a Pull Request!

## Development Setup
Go 1.20+ is required. DepScan relies on internal `extractor` wrappers (like Python's `ast` module and Node's `acorn` parser) which are packaged in the repository, so no external heavy AST libraries are required for the host machine aside from standard Go tooling.
