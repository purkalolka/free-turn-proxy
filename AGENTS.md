# AGENTS.md

## Repository Overview
Free Turn Proxy is a tunnel proxy encapsulating UDP/TCP traffic over TURN/WebRTC protocols to bypass network restrictions. It supports multiple transports, DTLS, packet obfuscation, and WireGuard/AmneziaWG integration.

### Core Modules & Subsystems
- `cmd/client`, `cmd/server`: Host binary entrypoints.
- `internal/`: Core proxy engine and protocols:
  - `config`: CLI and JSON configuration parsing/validation.
  - `wire`: Packet encapsulation, codecs, and obfuscation (`rtpopus`, `rtpopus2`, `rtpopus3`, `shape`).
  - `proxy`: UDP/TCP relay engines (`udprelay`, `udpserver`, `tcprelay`, `tcpserver`).
  - `provider`: TURN credential providers (VK and multi-provider).
  - `tunnel`: WireGuard/AmneziaWG userspace tun bindings (`bind`, `wgconf`, `awg`).
- `mobile/`: Exported Go mobile API (`gomobile bind`) for Android (AAR) and iOS (XCFramework).
- `desktop/`: Wails v2 desktop application:
  - `desktop/backend`: Go backend, state store, SSH server provisioning engine.
  - `desktop/backend/wintunnel`: Windows wintun driver integration.
  - `desktop/frontend`: React 19 + TypeScript + Vite UI.

---

## Build & Test Commands

### Go Toolchain Quirks
- **Go version**: Go >= 1.26 required.
- **Linker flag requirement**: Must supply `-checklinkname=0` in `-ldflags` when building or binding (`-ldflags "-s -w -checklinkname=0 -X main.version=..."`).

### Building Binaries
Build host binaries (`dist/client.exe`, `dist/server.exe`):
```powershell
go build -ldflags "-s -w -checklinkname=0 -X main.version=dev" -trimpath -o dist/client.exe ./cmd/client
go build -ldflags "-s -w -checklinkname=0 -X main.version=dev" -trimpath -o dist/server.exe ./cmd/server
```
*(If `task` runner is installed: `task build`)*

### Testing
- Run all unit tests:
  ```powershell
  go test -count=1 ./...
  ```
  *(Note: `-race` requires GCC/MinGW CGO. When CGO or race detector is enabled, test execution may be slow on Windows; run package-specific tests when iterating.)*
- Run a single package:
  ```powershell
  go test -v -count=1 ./internal/config/...
  go test -v -count=1 ./desktop/backend/serversetup/...
  ```
- Run a specific test:
  ```powershell
  go test -v -run TestImportLinkWithWgConfSwitchesToVPN ./desktop/backend
  ```
- Integration tests in `desktop/backend/serversetup/`:
  - Default to skipped unless `FT_TEST_SSH_HOST` is provided.
  - Tests that mutate remote VPS software require `FT_TEST_MUTATE=1`.

### Desktop Frontend
Located in `desktop/frontend`:
- Install dependencies: `npm --prefix desktop/frontend install`
- Typecheck & build: `npm --prefix desktop/frontend run build`
- Dev server: `npm --prefix desktop/frontend run dev`
- Full desktop app build (requires Wails CLI): `cd desktop && wails build`

### Verification Pipeline (Local CI)
Standard order before committing changes:
1. Format check: `gofmt -l .` (fix with `gofmt -w .`)
2. Go vet: `go vet ./...`
3. Lint (if `golangci-lint` installed): `golangci-lint run --timeout=5m ./...`
4. Tests: `go test -count=1 ./...`
*(Or `task ci` if Taskfile toolchain is installed).*

---

## Key Development Constraints & Gotchas
- **Windows / Cross-platform Paths**: Avoid hardcoded POSIX paths in scripts or Go tests touching the filesystem; use `filepath.Join` and handle CRLF vs LF appropriately.
- **Wails Bindings**: Types exposed to frontend from `desktop/app.go` or `desktop/backend` generate bindings under `desktop/frontend/wailsjs`. Keep backend API methods clean and serializable.
- **SSH / Server Control Scripts**: Scripts deployed to remote VPS live in `desktop/backend/serversetup/server-control/src/`. Edits there must maintain POSIX shell compatibility (`/bin/sh` / bash).
- **Mobile Bindings**: `mobile/api.go` must adhere to `gomobile` restrictions (only exported functions/types compatible with Go mobile spec; primitive types, byte slices, or strings).
