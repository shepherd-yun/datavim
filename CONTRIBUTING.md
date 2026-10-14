# Contributing

Thanks for helping with sqlvim! Bug reports, ideas and pull requests are welcome.

## Development

Go (the version in `go.mod`) and, for integration tests, Docker.

    make test               # unit tests (with -race)
    make test-integration   # MySQL 8 and PostgreSQL 16 in containers
    make lint               # golangci-lint, gofumpt, goimports
    make run

## Pull requests

- One topic per pull request; tests first (the code is written test-first).
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(ui): …`, `fix(db): …`, `docs: …`.
- User-visible changes get a line under `[Unreleased]` in `CHANGELOG.md`.
- UI text is English. Packages live under `internal/` and have no public API.
- Never log or print passwords.

## Design

The overall design is in `docs/superpowers/specs/2026-10-13-sqlvim-design.md`;
each release has an implementation plan in `docs/superpowers/plans/`.
