# tether

**Tether your AI coding agents to the Quilr LLM Gateway.**

`tether` connects AI coding agents to the **Quilr LLM Gateway**. Today it supports
**Claude Code**; the profile model and command set are designed so other agents
can be added later.

For Claude Code, `tether` switches between direct Anthropic, the **Quilr LLM Gateway**
and **Amazon Bedrock**. It edits `settings.json` safely: it changes only the keys
it owns, backs up the file before every write, and keeps keys out of the file.
It also generates `managed-settings.json` for Intune and Jamf rollouts.

It ships as a single static binary for Windows, macOS and Linux (amd64 and arm64).
Target machines don't need Python, Go or any other runtime.

## Install

Download the binary for your platform from `dist/` (or your release page), rename
it to `tether` (`tether.exe` on Windows), and put it on your `PATH`:

```sh
# macOS / Linux
install -m 755 tether-darwin-arm64 /usr/local/bin/tether      # pick your OS/arch
```
```powershell
# Windows
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\Programs\tether" | Out-Null
Copy-Item tether-windows-amd64.exe "$env:LOCALAPPDATA\Programs\tether\tether.exe"
# then add that folder to your user PATH
```

Install the binary **before** running `tether use`. The `apiKeyHelper` it writes
points at the binary's path, so moving it later means re-running `tether use`.

### Build from source

With Go 1.26+ installed, `go install` builds and installs it in one step:

```sh
go install github.com/gurmukhnishansingh-quilr/quilr-tether/cmd/tether@latest
```

You need Go 1.26+. The build scripts cross-compile every target into `dist/` with
`CGO_ENABLED=0`, so one machine can build them all:

```sh
./build.sh                 # or: pwsh ./build.ps1
go test ./...              # run the tests
```

## Quick start: Quilr India

```sh
# 1. Add a profile. The key is read without echo and stored in the OS keychain.
tether profile add quilr-india --type quilr --region india-1 \
     --email you@company.com --label claude-code
#   (non-interactive:  printf '%s\n' "$QUILR_KEY" | tether profile add ... --key-stdin)

# 2. Preview the change, then apply it. tether backs up first and asks before writing.
tether diff quilr-india
tether use quilr-india

# 3. Check it end to end.
tether doctor
```

`use` writes the following (other keys in the file are left as they are):

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://guardrails-india-1.quilr.ai/anthropic_messages",
    "ANTHROPIC_CUSTOM_HEADERS": "X-User-Email: you@company.com\nX-Provider-Label: claude-code",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"
  },
  "apiKeyHelper": "/usr/local/bin/tether key quilr-india"
}
```

Claude Code runs `tether key quilr-india` and uses its output as the key. The key
itself stays in the keychain (macOS Keychain, Windows Credential Manager or Linux
Secret Service). `tether key` refuses to print to a terminal, so only a pipe, such
as Claude Code, can read it.

Regions: `auto`, `usa-1`, `usa-2`, `india-1`, `jp-1`, or `--base-url` for anything else.

## Commands

| Command | What it does |
|---|---|
| `profile add <name> --type quilr\|anthropic\|bedrock` | Create a profile. Prompts for anything missing when run in a terminal. |
| `profile list \| show <name> \| remove <name>` | Manage profiles. Keys are always masked (`sk-quilr-…abcd`). |
| `use <name> [--plaintext]` | Apply a profile, with backup, diff preview and confirmation. |
| `diff <name>` | Show what `use` would change without writing anything. |
| `status [--json]` | Show the active profile, owned keys, the merged view across scopes, and conflicts. |
| `doctor [<name>] [--json]` | Run live gateway checks (see below). Exits 1 if any check fails. |
| `models [<name>] [--refresh] [--json]` | List gateway models, marking which are pinned, allowed or dropped. |
| `pin <name> --opus/--sonnet/--haiku/--fable ID` | Set `ANTHROPIC_DEFAULT_*_MODEL`. Pass `''` to unpin. |
| `allow <name> --models a,b,c [--enforce]` | Set `availableModels` (and `enforceAvailableModels` in managed scope). |
| `override <name> <anthropic-id>=<gateway-id> ...` | Set `modelOverrides`. |
| `export-managed <name> -o file [--plist f] [--lock-provider]` | Write a rollout file for Intune or Jamf. |
| `restore [timestamp] [--list]` | Restore a backup. Defaults to the latest backup for `--scope`. |

`pin`, `allow` and `override` edit the profile. Run `tether use <name>` afterwards to apply the change.

Every command takes `--scope user|project|local|managed` (default `user`),
`--project-dir`, `--yes`, `--no-color` and `--verbose`. Flags can come before or
after positional arguments.

**Exit codes:** 0 ok · 1 doctor failure · 2 usage error · 3 IO or permission error.
The managed scope needs admin rights. Without them, tether exits 3 with a hint to use
`sudo` or an elevated shell, and writes nothing.

### Keys

- The default is `apiKeyHelper` plus the OS keychain. On a headless Linux machine
  with no Secret Service, the key goes to an owner-only file
  (`~/.config/tether/secrets/<name>.key`) and tether prints a warning.
  `TETHER_SECRET_BACKEND=file|keyring` forces a backend.
- `--plaintext` writes `env.ANTHROPIC_API_KEY` instead. tether refuses it for
  `project` scope (the file is committed to git) and for `managed` scope (the file
  is readable by every user).
- tether never sets `ANTHROPIC_AUTH_TOKEN` for Quilr, because Quilr's Anthropic
  route authenticates with `x-api-key`.

### Other providers

```sh
tether profile add direct --type anthropic           # clears every tether key -> claude.ai login / Console key
tether profile add bedrock-prod --type bedrock --aws-region us-east-1 --aws-profile prod \
     --sso-refresh --opus us.anthropic.claude-opus-4-8 --sonnet us.anthropic.claude-sonnet-4-6
tether profile add quilr-us --type quilr --region usa-1 --bedrock-backed   # gateway forwards to Bedrock
```

Switching profiles first clears every key tether owns. For example, a leftover
`CLAUDE_CODE_USE_BEDROCK` can't survive a switch to Quilr, where it would silently
turn off gateway model discovery.

## `tether doctor`

| # | Check | Fails when |
|---|---|---|
| 1 | Settings files | A file isn't valid JSON, or provider variables conflict (for example `CLAUDE_CODE_USE_BEDROCK` together with `ANTHROPIC_BASE_URL`, or `ANTHROPIC_AUTH_TOKEN` with Quilr) |
| 2 | Precedence | A shell-exported `CLAUDE_CODE_USE_*` applies because no file sets it. Warns about other shell variables and about project, local or managed values that outrank your scope |
| 3 | Inference | `POST /v1/messages` with `max_tokens: 16` doesn't return 200 |
| 4 | Streaming | The response isn't `text/event-stream` or has no events. Warns when every event arrives at once, which means the gateway buffers |
| 5 | Beta passthrough | `anthropic-beta` gets a 400. This is only a warning for `--bedrock-backed` profiles |
| 6 | Token counting | Never fails; `count_tokens` is optional, so a 404 is a warning |
| 7 | Model discovery | `GET /v1/models?limit=1000` redirects, is slower than the 3 s budget (or the configured timeout), or has the wrong shape. Lists what `/model` will show and which IDs Claude Code drops |
| 8 | Pinned models | Never fails. Warns about pinned or overridden IDs the key can't see |
| 9 | Log tag | Every request carries `X-Conversation-Id: tether-doctor-<timestamp>`, so you can find the run in the Quilr logs |

All HTTP requests honour `HTTPS_PROXY` and never follow redirects.

## Team rollout

```sh
tether allow quilr-india --models claude-sonnet-4-6,claude-opus-4-8
tether export-managed quilr-india -o managed-settings.json --plist com.anthropic.claudecode.plist \
     --enforce --lock-provider --api-key-helper "/usr/local/bin/tether key quilr-india"
```

The export never contains a key. It drops the per-user `X-User-Email` header
unless you pass `--keep-user-email`. Without `--api-key-helper`, it adds a
`"__key_delivery"` note field instead (`--no-key-note` removes it).
`--lock-provider` adds `allowedProviders: ["customEndpoint"]`, which needs
Claude Code v2.1.285+. The command prints Intune and Jamf deployment notes for:

- macOS: `/Library/Application Support/ClaudeCode/managed-settings.json`
- Windows: `C:\Program Files\ClaudeCode\managed-settings.json`
- Linux: `/etc/claude-code/managed-settings.json`

On macOS you can deploy the generated plist instead, as a configuration profile
for the `com.anthropic.claudecode` domain.

## Files

| What | Where |
|---|---|
| Profiles | `~/.config/tether/profiles.toml` (`%APPDATA%\tether\profiles.toml` on Windows). Override with `TETHER_CONFIG_DIR` |
| Backups | `~/.claude/backups/settings.<timestamp>.json` plus a `.meta` sidecar. The last 20 are kept |
| Model cache | `<config>/cache/models-<profile>.json` |

`CLAUDE_CONFIG_DIR` is honoured the same way Claude Code honours it. See
[DESIGN.md](DESIGN.md) for ownership and precedence rules.
