# Contributing to OpsPulse

## Development Setup

### Prerequisites

- Go 1.24+
- golangci-lint
- Docker (optional)

### Build

```bash
make build
```

### Test

```bash
make test
```

### Lint

```bash
make lint
```

## Commit Convention

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add server inventory
fix: handle SSH timeout
docs: update README
test: add executor tests
ci: update GitHub Actions
```

## Pull Requests

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run `make lint && make test`
5. Submit a pull request
