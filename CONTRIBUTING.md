# Contributing to go-adbtest

## Development Setup

1. Clone the repository:
   ```sh
   git clone git@github.com:GyldendalDigital/go-adbtest.git
   cd go-adbtest
   ```

2. Ensure Go 1.23+ is installed:
   ```sh
   go version
   ```

3. For integration tests, set up an Android emulator:
   - Install Android SDK with `platform-tools`
   - Create an AVD (e.g., `Pixel_7` with API 35)
   - Ensure `adb` is in your PATH or `$ANDROID_HOME` is set

## Running Tests

Unit tests (no emulator required):
```sh
go test ./...
```

Integration tests (requires a running emulator):
```sh
go test -tags android_integration -timeout 5m ./...
```

## Code Style

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Run `gofmt` before committing — all code must be formatted
- Run `go vet ./...` — no issues allowed
- Only comment code that needs clarification; let the code speak for itself

## Commit Messages

Format: `type(scope): description`

Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `ci`

Examples:
```
feat(adb): implement Shell and Install methods
fix(cdp): handle WebSocket reconnection after app restart
chore: bump github.com/coder/websocket to v1.8.15
docs: add CI integration example to README
```

## Branch Model

- `master` — main branch, always working
- `feature/*` — new functionality
- `fix/*` — bug fixes
- `chore/*` — maintenance

One branch per work package. Rebase onto `master` before pushing.

## Error Handling

- Library internals return `error`
- Functions accepting `testing.TB` call `t.Fatal` on error
- No panics in library code (exception: `Setup()` in `TestMain` context)

## Pull Requests

- One PR per task from the work breakdown
- Include tests alongside implementation
- Ensure `go test ./...` and `go vet ./...` pass
- Reference the task ID in the PR description
