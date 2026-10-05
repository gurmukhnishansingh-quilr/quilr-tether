# tether design notes

`tether` connects AI coding agents to LLM gateways, starting with Quilr. Claude Code is the first agent it
supports. The Claude Code specifics (settings paths, owned keys, discovery
rules) live in `internal/app`; adding another agent means adding its own
settings adapter and ownership list, while profiles, key storage, backups,
`doctor` and the gateway client stay shared.

## Why Go

The tool has to run on developer laptops and MDM-managed machines that may not
have Python. Go cross-compiles to one static binary per OS and architecture
(`CGO_ENABLED=0`), so nothing needs to be installed at runtime. There are three
small dependencies:

- `zalando/go-keyring` for the OS keychain
- `BurntSushi/toml` for `profiles.toml`
- `golang.org/x/term` for hidden key input and TTY detection

Everything else uses the standard library, including HTTP, which is `net/http`
with redirects disabled.

## Ownership

tether owns a fixed set of keys (`internal/app/ownership.go`) and never touches anything else.

- **Top level:** `model`, `availableModels`, `enforceAvailableModels`,
  `modelOverrides`, `apiKeyHelper`, `awsAuthRefresh`.
  `enforceAvailableModels` was added to the original list because
  `allow --enforce` writes it, so a switch must also be able to clear it.
- **`env`:** `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`,
  `ANTHROPIC_CUSTOM_HEADERS`, `ANTHROPIC_MODEL`,
  `ANTHROPIC_DEFAULT_<TIER>_MODEL` (plus `_NAME`, `_DESCRIPTION` and
  `_SUPPORTED_CAPABILITIES`), `ANTHROPIC_CUSTOM_MODEL_OPTION*`,
  `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`,
  `CLAUDE_CODE_GATEWAY_MODEL_DISCOVERY_TIMEOUT_MS`,
  `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS`, `CLAUDE_CODE_USE_BEDROCK`,
  `AWS_REGION`, `AWS_PROFILE`.

`use` computes `ApplyFragment(current, profileFragment)`:

1. Every owned key is cleared, then the profile's keys are applied. Stale
   provider variables can't survive a switch.
2. Keys tether doesn't own keep their value **and their position**. tether parses
   JSON with an order-preserving parser (`internal/ojson`), because Go maps
   don't keep order. An owned key that the new profile also sets stays where
   it was; new keys are appended.
3. The `env` block is removed only when tether itself emptied it. A user's own
   `"env": {}` stays.
4. The file's existing indent style (2 spaces, 4 spaces or tabs) is preserved.

Switching to an `anthropic` profile therefore returns the file to exactly what
the user had before tether touched it. The tests check this byte for byte.

**Detecting the active profile.** tether stores no state. `status`, `models` and
`doctor` rebuild each profile's fragment and compare it with the owned keys in
the file. Hand edits show up as "none matches".

## MCP servers

`tether mcp` adds Quilr MCP Gateway servers to Claude Code. Only user scope is
supported: the top-level `mcpServers` object in `~/.claude.json`
(`$CLAUDE_CONFIG_DIR/.claude.json` when set). That file is Claude Code's global
config, not a settings file, so `--scope` other than `user` is refused.

- **Ownership is per entry, not per key.** MCP servers are additive, so the
  "clear every owned key" model above doesn't apply. tether owns an
  `mcpServers` entry when its name is in `mcp.toml` and it is the entry tether
  would write, or its `headersHelper` ends in `mcp-headers <name>`. `mcp use`
  refuses to replace any other entry of the same name without `--force`;
  `mcp remove` leaves such an entry alone. tether deletes `mcpServers` only
  when removing its own last entry.
- **Only `mcpServers` is diffed.** `~/.claude.json` is large and holds Claude
  Code state. The preview shows `mcpServers` alone, with secret `headers` and
  `env` values of other servers masked. The whole file is still backed up
  first, re-serialized with key order and number formatting preserved, and
  `restore --mcp` puts it back.
- **Tokens go through `headersHelper`.** `mcp-headers <name>` prints
  `{"Authorization": "Bearer …", "mcpuser": "…"}`. The token sits in the keychain
  under `mcp.<name>`. Profile names can't contain `.`, so this never collides
  with an LLM profile's key. OAuth entries carry no headers; Claude Code signs
  in through `/mcp`.
- **`mcp.toml` is separate from `profiles.toml`**, because older tether
  versions reject unknown fields in `profiles.toml`.
- **`mcp doctor`** speaks Streamable HTTP: `initialize` (JSON or SSE reply,
  `Mcp-Session-Id` kept), `notifications/initialized`, then `tools/list`. It
  mirrors Claude Code's managed controls: `managed-mcp.json` takes exclusive
  control, and `allowedMcpServers`/`deniedMcpServers` match by `serverName` or
  `serverUrl` wildcard (case-insensitive).

Claude Code rewrites `~/.claude.json` while it runs, so a write can race with a
running session. tether keeps the read-modify-write short, and the README says to
close Claude Code first.

## Writes

Every write follows the same sequence:

1. Preflight: create and delete a probe file in the target directory. If that
   fails (managed scope without admin), tether exits 3 before it writes anything,
   including the backup.
2. Back up to `<claude home>/backups/settings.<UTC ts>.json` (mode 0600, since a
   backup can hold a plaintext key). A `.meta` sidecar records the source path,
   the scope and whether the file existed. Restoring an "absent" backup deletes
   the file. Only the 20 newest backups are kept.
3. Write a temp file in the same directory, fsync it, then rename it over the
   target. On Windows the rename is retried briefly, because an editor or
   Claude Code's file watcher can hold the file open. On POSIX the directory is
   fsynced too.

`restore` backs up the current file first, so a restore can be undone.

## Secrets

- Keys live in the OS keychain, or in an owner-only file when there is no
  keychain. Each profile records which backend holds its key. They never appear
  in `profiles.toml`, in `project` or `managed` settings, or in exports.
- `apiKeyHelper` is `<tether binary> key <profile>`. On Windows the binary path
  uses forward slashes so the same string works in cmd.exe, PowerShell and Git
  Bash. When `tether` on `PATH` is the same file as the running binary, tether uses
  the `PATH` location, so a Homebrew-style symlink survives upgrades.
  `tether key` refuses to write to a terminal.
- All output goes through `Redact`, which masks every key tether has loaded and
  anything shaped like `sk-xxx-…`. This covers keys echoed back in gateway
  error bodies.
- `--plaintext` is refused for `project` scope (committed to git) and `managed`
  scope (readable by every user). `export-managed` runs `AssertNoSecrets` before
  writing: it rejects credential env names, the stored key, and anything
  key-shaped.

## Precedence

Claude Code resolves settings from highest to lowest precedence:

1. **managed** (`managed-settings.json`, MDM or the console)
2. `--settings` on the command line (per invocation, so tether can't see it)
3. **local** (`.claude/settings.local.json`)
4. **project** (`.claude/settings.json`)
5. **user** (`~/.claude/settings.json` or `$CLAUDE_CONFIG_DIR`)

Top-level keys come from the highest scope that sets them. `env` merges key by
key. `status` shows each effective value with the scope it came from, and
`use` warns when a higher scope overrides the scope being written.

**Shell environment.** The original spec said a shell `ANTHROPIC_BASE_URL`
overrides the file. Current Claude Code docs say the opposite: "the settings
file value applies in most sessions" because Claude Code writes each `env`
entry into the process environment
([env-vars](https://code.claude.com/docs/en/env-vars)). So tether treats the
runtime environment as the shell overlaid with the merged settings `env`:

- A shell variable the files **don't** set still applies. A shell-exported
  `CLAUDE_CODE_USE_*` therefore fails `doctor`: it turns off gateway discovery
  and reroutes inference, even after a clean `use`.
- A shell variable the files **do** set is reported as a warning, because tools
  that read the shell directly still see the shell value.

**Discovery.** Claude Code skips gateway discovery when any `CLAUDE_CODE_USE_*`
variable is set. It treats a redirect on `/v1/models` as failure, and keeps only
IDs containing `claude` or `anthropic` (case-insensitive). `doctor` mirrors all
three rules.

## Managed paths

Verified against <https://code.claude.com/docs/en/managed-settings> (October 2026):

| OS | Path |
|---|---|
| macOS | `/Library/Application Support/ClaudeCode/managed-settings.json` (or the `com.anthropic.claudecode` plist domain) |
| Windows | `C:\Program Files\ClaudeCode\managed-settings.json` (or `HKLM\SOFTWARE\Policies\ClaudeCode` `Settings`). The legacy `C:\ProgramData` path is no longer read. |
| Linux / WSL | `/etc/claude-code/managed-settings.json` |

Claude Code reads `enforceAvailableModels` only from managed settings, so tether
writes it only for `--scope managed` and in `export-managed`.
`allowedProviders` needs Claude Code v2.1.285+.

## Testing

`go test ./...` runs against a temp sandbox. It sets `CLAUDE_CONFIG_DIR`,
`TETHER_CONFIG_DIR` and `TETHER_MANAGED_DIR`, uses the file secret backend, and
injects an empty shell environment. Gateway calls go to an in-process
`httptest` server, so no test touches the network or the real keychain.
