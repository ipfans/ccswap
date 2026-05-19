# ccswap

Multi-account switcher for [Claude Code](https://docs.anthropic.com/en/docs/claude-code). Manage multiple Claude accounts and switch between them — manually or automatically based on token usage.

Single binary, no runtime dependencies. Data format compatible with [claude-swap](https://github.com/thiagobutignon/claude-swap) (Python version, Linux/WSL).

## Install

```sh
go install github.com/ipfans/ccswap/cmd/ccswap@latest
```

Or build from source:

```sh
git clone https://github.com/ipfans/ccswap.git
cd ccswap
go build -o ccswap ./cmd/ccswap/
```

## Quick Start

Log in to your first Claude Code account, then:

```sh
ccswap add-account        # capture current account
# log in to another account in Claude Code...
ccswap add-account        # capture second account

ccswap list               # show all accounts with usage
ccswap switch             # rotate to next account
ccswap auto               # auto-switch if current account is over threshold
```

## Commands

| Command | Description |
|---------|-------------|
| `add-account` | Add current Claude Code login to the managed pool |
| `remove-account <num\|email>` | Remove an account by number or email |
| `list` | List all accounts with usage (5h/7d windows) |
| `status` | Show active account identity and usage detail |
| `switch` | Rotate to the next account in sequence |
| `switch-to <num\|email>` | Switch to a specific account |
| `auto [--threshold N]` | Auto-switch if usage exceeds threshold (default 80%) |

## Auto-Switch

`auto` checks the current account's token usage against a threshold. If either the 5-hour or 7-day window exceeds the threshold, it finds the first healthy account in sequence and switches to it.

```sh
ccswap auto                  # default threshold 80%
ccswap auto --threshold 90   # more aggressive
```

This is a one-shot command. For periodic checks, use cron or a shell wrapper:

```sh
# crontab: check every 5 minutes
*/5 * * * * /path/to/ccswap auto --threshold 80
```

## How It Works

- Accounts are stored in `~/.claude-swap/sequence.json`
- Credentials are backed up as base64-encoded files in `~/.claude-swap/credentials/`
- Config backups live in `~/.claude-swap/configs/`
- Switching replaces `~/.claude/.credentials.json` and the `oauthAccount` field in `~/.claude.json`
- A file lock (`~/.claude-swap/.lock`) prevents concurrent operations
- Usage queries are cached for 30 seconds (`list`/`status`); `auto` always queries live data

## Migrating from claude-swap (Python)

On **Linux/WSL**: ccswap reads the same `sequence.json` and backup files — just install and use.

On **macOS/Windows**: the Python version stores backups in the system keychain. Run `ccswap add-account` for each account to re-capture credentials as files.

## License

MIT
