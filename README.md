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

tether is a single prebuilt binary. You don't need Go, Python or any other runtime.

**macOS / Linux (Homebrew)**
```sh
brew install gurmukhnishansingh-quilr/tap/tether
```

**Windows (winget)**
```powershell
winget install Quilr.Tether
```

**Install script** (no package manager needed; checks the SHA-256 against the release)
```sh
# macOS / Linux: installs to /usr/local/bin, or ~/.local/bin without write access
curl -fsSL https://raw.githubusercontent.com/gurmukhnishansingh-quilr/quilr-tether/main/install.sh | sh
```
```powershell
# Windows: installs to %LOCALAPPDATA%\Programs\tether and adds it to your PATH (no admin needed)
irm https://raw.githubusercontent.com/gurmukhnishansingh-quilr/quilr-tether/main/install.ps1 | iex
```
Set `TETHER_VERSION=v0.1.0` to pin a version, or `TETHER_INSTALL_DIR` to choose where it goes.

**Manual:** download the archive for your platform from
[Releases](https://github.com/gurmukhnishansingh-quilr/quilr-tether/releases), extract
`tether` (`tether.exe` on Windows) and put it on your `PATH`.

Upgrade with `brew upgrade tether`, `winget upgrade Quilr.Tether`, or by re-running the script.
The `apiKeyHelper` that `tether use` writes points at the installed command, so
upgrades keep working. If you move the binary by hand, re-run `tether use`.

### Build from source

Only needed if you're developing tether. Requires Go 1.26+:

```sh
go install github.com/gurmukhnishansingh-quilr/quilr-tether/cmd/tether@latest
# or, from a checkout:
go test ./...
./build.sh                 # cross-compiles every platform into dist/ (or: pwsh ./build.ps1)
```

### Publishing a release

Pushing a tag runs `.github/workflows/release.yml` (GoReleaser). It builds every
platform, attaches the archives, `checksums.txt` and both install scripts to a
GitHub release, updates the Homebrew tap, and opens a PR to `microsoft/winget-pkgs`:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

Homebrew and winget publishing need a repository secret named `RELEASE_TOKEN`.
It's a GitHub personal access token with `public_repo` scope, which can push to
`gurmukhnishansingh-quilr/homebrew-tap` and to the `winget-pkgs` fork. Without
it, the release still publishes and only the brew/winget steps are skipped. Add
the secret, then re-run the workflow for the same tag. The first winget
submission is reviewed by Microsoft, which usually takes a few days; until then,
use the install script.

Check the config locally with `goreleaser check && goreleaser release --snapshot --clean`.

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

### One-line setup

Add the profile and apply it in one command. `--yes` skips the y/N question; tether
still backs up `settings.json` first, so `tether restore` undoes it.

```sh
# macOS / Linux / PowerShell 7
tether profile add quilr --type quilr --region auto --email you@company.com --label claude-code && tether use quilr --yes
```
```powershell
# Windows PowerShell 5.1 (no &&)
tether profile add quilr --type quilr --region auto --email you@company.com --label claude-code; tether use quilr --yes
```

Install and configure together:

```sh
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/gurmukhnishansingh-quilr/quilr-tether/main/install.sh | sh && export PATH="$HOME/.local/bin:/usr/local/bin:$PATH" && tether profile add quilr --type quilr --region auto --email you@company.com --label claude-code && tether use quilr --yes
```
```powershell
# Windows
irm https://raw.githubusercontent.com/gurmukhnishansingh-quilr/quilr-tether/main/install.ps1 | iex; tether profile add quilr --type quilr --region auto --email you@company.com --label claude-code; tether use quilr --yes
```

`--region auto` points at `https://guardrails.quilr.ai/anthropic_messages`. It is
also the default, so you can leave `--region` out.

### Passing the API key

With none of these options, `tether profile add` prompts for the key with hidden input.
Use only one of them:

| Option | How the key is passed | When to use it |
|---|---|---|
| `--key sk-quilr-...` | Directly on the command line | Quickest, but it lands in your shell history (tether warns) |
| `--key-env VAR` | Read from an environment variable | Scripts and CI; nothing in the command itself |
| `--key-stdin` | Read from standard input | Piping from a file or a secrets manager |
| *(none)* | Hidden prompt | Interactive use; the safest option |

```sh
# Key on the command line
tether profile add quilr --type quilr --region auto --email you@company.com --label claude-code --key sk-quilr-XXXX && tether use quilr --yes

# Key from an environment variable (macOS / Linux)
export QUILR_KEY=sk-quilr-XXXX
tether profile add quilr --type quilr --region auto --email you@company.com --label claude-code --key-env QUILR_KEY && tether use quilr --yes

# Key piped in
cat ~/quilr.key | tether profile add quilr --type quilr --region auto --key-stdin && tether use quilr --yes
```
```powershell
# Key from an environment variable (Windows PowerShell)
$env:QUILR_KEY = "sk-quilr-XXXX"
tether profile add quilr --type quilr --region auto --email you@company.com --label claude-code --key-env QUILR_KEY; tether use quilr --yes
```

However it's passed, the key is stored in the OS keychain, never in
`settings.json`, and output only ever shows it masked (`sk-quilr-…XXXX`).

To replace the key later, for example after rotating it, without changing anything else:

```sh
tether profile set-key quilr                          # hidden prompt
tether profile set-key quilr --key-env QUILR_KEY      # or --key / --key-stdin
```

The key must be created in Quilr for the **`anthropic_messages`** provider
(`anthropic_messages_bedrock` or `anthropic_messages_azure` if the gateway forwards
to Bedrock or Azure). A key created for the plain `anthropic` provider is rejected
with *"This API key is configured for 'anthropic' provider"*, and `tether doctor`
says so.

### Profile options

| Option | What it sets | Notes |
|---|---|---|
| `--region` | Gateway URL | `auto`, `usa-1`, `usa-2`, `india-1` or `jp-1` |
| `--base-url URL` | Gateway URL | Instead of `--region`, for any other endpoint |
| `--email` | `X-User-Email` header | Optional |
| `--label` | `X-Provider-Label` header | Optional |
| `--no-discovery` | Model discovery off | On by default; fills `/model` with the gateway's models |
| `--bedrock-backed` | Experimental betas off | Set it when the gateway forwards to Bedrock |
| `--sonnet` / `--opus` / `--haiku` / `--fable ID` | Pinned models | Optional |

### Everyday use

```sh
tether status                     # active profile and any conflicting settings
tether doctor                     # live check: auth, streaming, model list, pins
tether models                     # the models /model will show
tether profile list               # all profiles; * marks the active one
tether use quilr-us               # switch to another region or gateway
tether restore                    # undo the last change
tether pin quilr --sonnet claude-sonnet-4-6 && tether use quilr
```

To apply a profile to one project instead of everywhere, add `--scope local`
(writes the git-ignored `<repo>/.claude/settings.local.json`).

## Commands

| Command | What it does |
|---|---|
| `profile add <name> --type quilr\|quilr-bedrock\|anthropic\|bedrock` | Create a profile. Prompts for anything missing when run in a terminal. |
| `profile list \| show <name> \| remove <name>` | Manage profiles. Keys are always masked (`sk-quilr-…abcd`). |
| `profile set-key <name>` | Replace a profile's stored key, keeping its other settings. |
| `use <name> [--plaintext]` | Apply a profile, with backup, diff preview and confirmation. |
| `diff <name>` | Show what `use` would change without writing anything. |
| `status [--json]` | Show the active profile, owned keys, the merged view across scopes, and conflicts. |
| `doctor [<name>] [--json]` | Run live gateway checks (see below). Exits 1 if any check fails. |
| `models [<name>] [--refresh] [--json]` | List gateway models, marking which are pinned, allowed or dropped. |
| `pin <name> --opus/--sonnet/--haiku/--fable ID` | Set `ANTHROPIC_DEFAULT_*_MODEL`. Pass `''` to unpin. |
| `allow <name> --models a,b,c [--enforce]` | Set `availableModels` (and `enforceAvailableModels` in managed scope). |
| `override <name> <anthropic-id>=<gateway-id> ...` | Set `modelOverrides`. |
| `export-managed <name> -o file [--plist f] [--lock-provider]` | Write a rollout file for Intune or Jamf. |
| `restore [timestamp] [--list] [--mcp]` | Restore a backup. Defaults to the latest backup for `--scope`; `--mcp` restores `~/.claude.json`. |
| `mcp add\|list\|show\|use\|diff\|doctor\|set-key\|remove` | Add Quilr MCP Gateway servers to Claude Code (user scope). See [MCP Gateway](#mcp-gateway). |

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

### Quilr Bedrock route (`--type quilr-bedrock`, experimental)

For a Quilr key created for the **`bedrock`** provider type. Claude Code runs in its
Bedrock mode against Quilr's `/bedrock-runtime` route and signs each request (AWS
SigV4) with the Quilr key:

```sh
tether profile add quilr-br --type quilr-bedrock --region auto --email you@company.com --label claude-code \
     --sonnet <bedrock-model-id> --haiku <bedrock-model-id> && tether use quilr-br --yes && tether doctor quilr-br
```

`use` writes `CLAUDE_CODE_USE_BEDROCK=1`, `ANTHROPIC_BEDROCK_BASE_URL=https://guardrails.quilr.ai/bedrock-runtime`,
`AWS_REGION` (`--aws-region`, default `us-east-1`) and the model pins, plus
`"awsCredentialExport": "<tether> aws-credentials quilr-br"`. Claude Code runs that
command to get the key from the keychain as AWS credentials (Claude Code v2.1.206+),
so the key is never written to `settings.json` or `~/.aws`.

- **Pin models.** The Bedrock route has no model listing, so pin Bedrock model IDs
  that are enabled on your key. `tether doctor` checks each pin.
- **Streaming must be enabled on the gateway.** Claude Code streams every response.
  If `doctor` check 4 reports *"Bedrock boto3 streaming is not enabled on this
  gateway"*, Claude Code can't run on this route until Quilr enables it. Use an
  `anthropic_messages` key (`--type quilr`) in the meantime.
- `--plaintext` and `tether models` don't apply to this type. `export-managed` turns
  `--api-key-helper CMD` into `awsCredentialExport`.

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

## MCP Gateway

tether also adds [Quilr MCP Gateway](https://docs.quilrai.dev/category/mcp-gateway)
servers to Claude Code. Only **user scope** is supported for now: the server goes
into the top-level `mcpServers` of `~/.claude.json` (`$CLAUDE_CONFIG_DIR/.claude.json`
when that is set) and loads in every project. Project (`.mcp.json`), local and
managed (`managed-mcp.json`) scopes are not supported yet.

```sh
# A single MCP, with an API token (Settings → API Tokens in Quilr). The token is
# read without echo and stored in the OS keychain.
tether mcp add quilr-github --slug github-prod --email you@company.com
#   (non-interactive:  printf '%s\n' "$QUILR_MCP_TOKEN" | tether mcp add ... --key-stdin)

# OneMCP: every MCP you may use, behind one endpoint. With --auth oauth, Claude
# Code signs in itself (/mcp) and tether stores no token.
tether mcp add quilr-one --onemcp --auth oauth

tether mcp diff quilr-github      # preview
tether mcp use quilr-github       # backs up ~/.claude.json, then asks before writing
tether mcp doctor quilr-github    # live check: initialize + tools/list
```

`mcp use` writes only its own entry. Other MCP servers and the rest of
`~/.claude.json` stay as they are:

```json
{
  "mcpServers": {
    "quilr-github": {
      "type": "http",
      "url": "https://mcpgateway.quilr.ai/github-prod/mcp",
      "headersHelper": "/usr/local/bin/tether mcp-headers quilr-github"
    },
    "quilr-one": {
      "type": "http",
      "url": "https://mcpgateway.quilr.ai/quilrone/mcp"
    }
  }
}
```

Claude Code runs `tether mcp-headers quilr-github`, which prints
`{"Authorization": "Bearer <token>", "mcpuser": "you@company.com"}`. The token stays
in the keychain, and `mcp-headers` refuses to print to a terminal.

| Option | Meaning |
|---|---|
| `--slug SLUG` | The MCP's slug, from its endpoint `https://<gateway>/<slug>/mcp` on the MCP card |
| `--onemcp` | Use the OneMCP endpoint (`/quilrone/mcp`) instead of `--slug` |
| `--domain quilr.ai\|quilrai.com` | Gateway domain (default `quilr.ai`). Or `--base-url URL` for any other gateway |
| `--auth token\|oauth` | `token` (default): API token through `headersHelper`. `oauth`: no token; sign in with `/mcp` in Claude Code |
| `--email` | Sent as `mcpuser`. Required for `--auth token`, except with `--onemcp`. Must be on an allowed company domain |
| `--key-stdin` / `--key-env VAR` / `--key TOKEN` | How the API token is passed, as for `profile add` |

Restart Claude Code after `mcp use`. To change the token, run `tether mcp set-key <name>`.
To remove the server from Claude Code and delete its token, run `tether mcp remove <name> --yes`.
`tether restore --mcp` puts `~/.claude.json` back as it was before tether's last change.

`mcp use` refuses to replace an existing `mcpServers` entry with the same name that
tether didn't create (pass `--force` to replace it). Claude Code also rewrites
`~/.claude.json` while it runs, so close Claude Code sessions before `mcp use` or
`mcp remove`.

`tether mcp doctor` checks:

| # | Check | Fails when |
|---|---|---|
| 1 | Claude config | The entry is missing from `~/.claude.json` or belongs to another server. Warns when it was edited by hand |
| 2 | Credentials | No token is stored (`--auth token`) |
| 3 | Managed policy | A `managed-mcp.json` exists (only its servers load), or `deniedMcpServers` / `allowedMcpServers` in managed settings blocks the server |
| 4 | Initialize | The MCP `initialize` request doesn't return 200: bad token, `mcpuser` not allowed, unknown slug, redirect. For `--auth oauth`, a 401 means "reachable, sign in with /mcp" and passes |
| 5 | Tools | `tools/list` fails. Warns when the MCP exposes no tools |

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

## Remove or uninstall

Do these in order. If you uninstall tether while Claude Code still points at it for
the key (`apiKeyHelper`), Claude Code can't get the key.

**1. Remove the gateway settings from Claude Code.** This switches back to your normal
claude.ai login or Console key and removes only the keys tether wrote:

```sh
tether profile add direct --type anthropic
tether use direct --yes
```

Or put `settings.json` back exactly as it was before tether's last change:

```sh
tether restore --yes              # latest backup
tether restore --list             # see all backups
tether restore 20261002T1430      # a specific one
```

If you applied the profile with `--scope local` or `--scope project`, add the same
`--scope` here.

**2. Remove the profile and its stored key:**

```sh
tether profile remove quilr --yes
```

This deletes the profile and its key from the OS keychain. tether warns if the
profile is still applied anywhere.

**3. Uninstall tether:**

| Installed with | Uninstall |
|---|---|
| Homebrew | `brew uninstall tether`, then optionally `brew untap gurmukhnishansingh-quilr/tap` |
| winget | `winget uninstall Quilr.Tether` |
| `install.sh` | `rm /usr/local/bin/tether` (or `rm ~/.local/bin/tether`) |
| `install.ps1` | `Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\tether"`, then remove that folder from your user PATH |

If you added MCP servers, remove each one before uninstalling, since Claude Code runs
tether for their headers: `tether mcp remove <name> --yes`.

**Optional cleanup** of profiles, the model cache and tether's settings backups. Keep
the backups if you might want an older `settings.json` back.

```sh
rm -rf ~/.config/tether ~/.claude/backups/settings.*        # macOS / Linux
```
```powershell
Remove-Item -Recurse "$env:APPDATA\tether"; Remove-Item "$env:USERPROFILE\.claude\backups\settings.*"   # Windows
```

**Everything in one command:**

```sh
# macOS / Linux (Homebrew install)
tether profile add direct --type anthropic && tether use direct --yes && tether profile remove quilr --yes && brew uninstall tether
```
```powershell
# Windows (winget install)
tether profile add direct --type anthropic; tether use direct --yes; tether profile remove quilr --yes; winget uninstall Quilr.Tether
```

## Files

| What | Where |
|---|---|
| Profiles | `~/.config/tether/profiles.toml` (`%APPDATA%\tether\profiles.toml` on Windows). Override with `TETHER_CONFIG_DIR` |
| Backups | `~/.claude/backups/settings.<timestamp>.json` plus a `.meta` sidecar. The last 20 are kept |
| Model cache | `<config>/cache/models-<profile>.json` |
| MCP servers | `<config>/mcp.toml` (no tokens), written into `~/.claude.json` by `tether mcp use` |

`CLAUDE_CONFIG_DIR` is honoured the same way Claude Code honours it. See
[DESIGN.md](DESIGN.md) for ownership and precedence rules.
