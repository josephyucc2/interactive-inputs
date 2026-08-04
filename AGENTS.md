# AGENTS.md

This file is the operational guide for AI coding agents working in this repository.

## Mission

This repository provides two related execution modes:

1. A GitHub Action that launches a temporary interactive-input portal for one workflow run.
2. A persistent HTTP server used by `Common_Client_CICD` workflows to create sessions through `POST /api/sessions`, show forms under `/portal/{id}`, and wait for results through `GET /api/sessions/{id}/wait`.

The persistent server on this Windows runner listens on TCP 9090.

## Start here

Before changing anything, run:

```powershell
git status --short --branch
git log -5 --oneline --decorate
Invoke-RestMethod http://127.0.0.1:9090/health
Get-ScheduledTask -TaskName "Interactive Inputs Server"
```

Expected state on the `JOSEPHYU` runner:

- Repository: `F:\CICD\interactive-inputs`
- Required branch: `feature/windows-runner-support`
- Server binary: `dist\action-windows-amd64.exe`
- Scheduled task: `Interactive Inputs Server`
- Local health URL: `http://127.0.0.1:9090/health`
- Current LAN URL can be found with the command below; do not hard-code the IP because DHCP may change it.

```powershell
$runnerIp = Get-NetIPAddress -AddressFamily IPv4 |
  Where-Object {
    $_.IPAddress -notmatch '^127\.' -and
    $_.PrefixOrigin -ne 'WellKnown' -and
    $_.InterfaceAlias -notmatch 'Loopback|vEthernet'
  } |
  Sort-Object InterfaceMetric |
  Select-Object -First 1 -ExpandProperty IPAddress

"http://${runnerIp}:9090"
```

If health is already `ok`, do not start a second server.

## Windows server operations

### Inspect

```powershell
Get-NetTCPConnection -LocalPort 9090 -State Listen
Get-ScheduledTask -TaskName "Interactive Inputs Server"
Get-ScheduledTaskInfo -TaskName "Interactive Inputs Server"
Invoke-RestMethod http://127.0.0.1:9090/health
```

### Start or restart the installed task

Restarting the process cancels all pending sessions because sessions are stored in memory. Check whether workflows are waiting for user input before restarting.

```powershell
Stop-ScheduledTask -TaskName "Interactive Inputs Server"
Start-ScheduledTask -TaskName "Interactive Inputs Server"
Invoke-RestMethod http://127.0.0.1:9090/health
```

The current non-administrator installation runs at user logon. It directly executes:

```text
F:\CICD\interactive-inputs\dist\action-windows-amd64.exe --server --port 9090 --session-timeout 300
```

### Install as a machine-wide startup task

The repository includes the preferred Windows installer. It requires an Administrator PowerShell terminal and installs the binary under `%ProgramData%`, runs it as `SYSTEM` at startup, and creates a LocalSubnet firewall rule.

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\scripts\install-windows.ps1 -Action Install -Port 9090
.\scripts\install-windows.ps1 -Action Status -Port 9090
```

Do not run the installer while another process owns port 9090. Do not uninstall or replace a working installation unless the user explicitly requests it.

## API smoke test

Use this after server or API changes. It creates a temporary session, verifies the portal, and cancels the session so no pending test session remains.

```powershell
$body = @{
  title = 'Agent smoke test'
  timeout = 60
  fields = @{
    fields = @(
      @{
        label = 'confirm'
        properties = @{
          display = 'Confirm'
          type = 'boolean'
          required = $true
          defaultValue = 'true'
        }
      }
    )
  }
} | ConvertTo-Json -Depth 8

$session = Invoke-RestMethod `
  -Method Post `
  -Uri http://127.0.0.1:9090/api/sessions `
  -ContentType application/json `
  -Body $body

Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:9090/portal/$($session.id)"
Invoke-WebRequest -UseBasicParsing -Method Post "http://127.0.0.1:9090/portal/$($session.id)/cancel"
Invoke-RestMethod "http://127.0.0.1:9090/api/sessions/$($session.id)"
```

The final status must be `cancelled`. The portal request must return HTTP 200.

## Development setup

The source is Go 1.22 under `src/`. JavaScript is only the GitHub Action launcher and packaging layer.

Important files:

- `src/main.go`: selects action mode or persistent `--server` mode.
- `src/internal/server/server.go`: persistent REST API, portal, upload, and health routes.
- `src/internal/session/session.go`: concurrency-safe in-memory session lifecycle.
- `src/internal/fields/fields.go`: field validation and `choicesFilePath` loading.
- `src/internal/web/`: portal handlers, templates, JavaScript, CSS, and static assets.
- `src/internal/runner/runner.go`: original single-run GitHub Action behavior.
- `invoke-binary.js`: selects the bundled executable for the current OS and CPU.
- `action.yml`: GitHub Action inputs and Node runtime declaration.
- `scripts/install-windows.ps1`: administrator-only persistent Windows installer.
- `.github/workflows/ci.yaml`: authoritative CI test and Windows cross-build commands.

This machine may not have Go installed. Check first:

```powershell
go version
node --version
```

Do not install toolchains without user approval. If Go is present, use:

```powershell
Push-Location src
go test ./...
go fmt ./...
go vet ./...
Pop-Location
```

Build Windows binaries from `src/`:

```powershell
Push-Location src
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go build -o ..\dist\action-windows-amd64.exe .
Pop-Location
```

When modifying Go files, run `go fmt` and `go test ./...`. When modifying platform selection or packaging, also verify `node invoke-binary.js` behavior or the relevant CI job.

## HTTP contract

Persistent mode exposes:

- `GET /health`
- `POST /api/sessions`
- `GET /api/sessions/{id}`
- `GET /api/sessions/{id}/wait?timeout=300`
- `GET /portal/{id}`
- `POST /portal/{id}/submit`
- `POST /portal/{id}/cancel`
- session-scoped upload/reset routes under `/portal/{id}/api/v1/`

`POST /api/sessions` must return HTTP 201 and JSON containing at least `id`, `status`, `portal_url`, `created_at`, and `expires_at`. The Common Client workflows expect the wait response to contain `status` and `results`.

Supported field types are `text`, `textarea`, `number`, `boolean`, `select`, `multiselect`, `file`, and `multifile`. If both inline `choices` and `choicesFilePath` are present, inline choices take precedence. Paths received through `choicesFilePath` are runner-local paths and must not be silently reinterpreted.

## Integration with Common_Client_CICD

The consumer repository is normally located at `F:\CICD\Common_Client_CICD`. Its workflows call:

```text
POST http://localhost:9090/api/sessions
GET  http://localhost:9090/api/sessions/{id}/wait?timeout=300
```

They display a LAN portal URL in the form `http://<runner-ip>:9090/portal/<session-id>`.

Be aware that most existing consumer workflows select `[self-hosted, macOS, ARM64]`, `fish`, or `slot`. A Windows runner with labels `[self-hosted, Windows, X64]` will not receive those jobs. Do not add false macOS labels to a Windows runner; adapt workflows explicitly if Windows execution is desired.

## Change rules

- Preserve both action mode and persistent server mode.
- Do not commit secrets, tokens, generated session data, logs, or machine-specific IP addresses.
- Do not expose the portal beyond the local subnet without explicit authorization and authentication review.
- Treat changes to session synchronization, channel closing, cleanup, uploads, or path handling as concurrency/security-sensitive.
- Keep API changes backward-compatible with `Common_Client_CICD`, or update and test the consumer in the same task.
- Do not edit bundled third-party assets under `src/internal/web/ui/static/libs/` unless the task specifically concerns vendored dependencies.
- Never overwrite unrelated user changes in a dirty worktree.
- Use `feature/windows-runner-support` until its Windows changes are merged into `main`; verify branch contents rather than assuming `main` contains Windows binaries.

## Completion checklist

Before handing work back:

1. Review `git diff --check` and `git status --short`.
2. Run the narrowest relevant tests; run all Go tests for Go changes when Go is available.
3. For persistent-server changes, run the API smoke test above.
4. Confirm both loopback health and the current LAN health URL return `{"status":"ok"}`.
5. If the server was restarted, state that pending in-memory sessions were cancelled.
6. Report files changed, tests run, and any test/toolchain limitation.
