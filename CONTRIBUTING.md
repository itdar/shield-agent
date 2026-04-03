# Contributing to shield-agent

## Development Setup

**Requirements:** Go 1.22+

```bash
git clone https://github.com/itdar/shield-agent.git
cd shield-agent
go build -o shield-agent ./cmd/shield-agent
```

## Running Tests

```bash
go test ./...
```

For verbose output:

```bash
go test -v ./...
```

## Branch Naming

| Type    | Pattern              |
|---------|----------------------|
| Feature | `feat/description`   |
| Bugfix  | `fix/description`    |
| Chore   | `chore/description`  |

Never push directly to `main`.

## Commit Convention

Format: `type(scope): description`

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `style`, `perf`

Examples:

```
feat(proxy): add X-Forwarded-For header support
fix(auth): handle expired token edge case
test(proxy): add concurrent request tests
```

Rules:
- Description in imperative mood, max 72 characters
- English only
- Include a body explaining *why* when the change is non-obvious

## Pull Request Process

1. Fork and create a branch from `main`
2. Make your changes and ensure all checks pass locally:
   ```bash
   go build ./...
   go test ./...
   go vet ./...
   ```
3. Open a PR against `main` with a title matching the commit convention
4. Summarize changes in bullet points in the PR description
5. Link any related issues

CI will run build, test, lint, and vet automatically.

## Code Style

- Follow existing patterns in the codebase
- Comments in English
- No dead code, debug prints, or TODOs left in committed code
- Keep functions focused and small
- Error messages should be lowercase and not end with punctuation

## Reporting Bugs / Requesting Features

Open a [GitHub Issue](https://github.com/itdar/shield-agent/issues) with:

- **Bug:** steps to reproduce, expected vs actual behavior, Go version and OS
- **Feature:** use case description and proposed behavior

Check existing issues before opening a new one.
