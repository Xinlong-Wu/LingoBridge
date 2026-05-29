# WeChatBox

WeChat Bot → LLM direct bridge. Connects WeChat bot accounts to OpenAI/Anthropic-compatible LLM APIs.

## Quick Start

### 1. Build

```bash
go build -o wechatbox ./cmd/wechatbox/
```

### 2. Configure

```bash
cp config.yaml.example ~/.wechatbox/config.yaml
# Edit ~/.wechatbox/config.yaml with your LLM API key and settings
```

Minimal config:

```yaml
llm:
  api_key: "sk-your-key-here"
```

### 3. Add a WeChat bot account

Scan the QR code with your WeChat app:

```bash
./wechatbox account new --name mybot
```

### 4. Run

```bash
./wechatbox run
```

Listens to all enabled accounts concurrently. Use `--account` to run a specific one:

```bash
./wechatbox run --account mybot
```

## CLI Reference

| Command | Description |
|---|---|
| `account new [--name <name>]` | Add a WeChat bot account via QR login |
| `account list` | List all accounts |
| `account delete <name>` | Delete an account |
| `run [--account <name>]` | Start the bot loop |

## In-Chat Commands

Send these as WeChat messages to the bot:

| Command | Description |
|---|---|
| `/new [name]` | Create a new conversation session |
| `/list` | List your sessions |
| `/switch <name>` | Switch active session |
| `/clear` | Archive current session, start fresh |

## Configuration

`~/.wechatbox/config.yaml`:

| Field | Default | Description |
|---|---|---|
| `llm.provider` | `openai` | `"openai"` or `"anthropic"` |
| `llm.base_url` | `https://api.deepseek.com/v1` | LLM API base URL |
| `llm.api_key` | — | Your API key (required) |
| `llm.model` | `deepseek-chat` | Model name |
| `llm.system_prompt` | `"You are a helpful assistant."` | System prompt |
| `llm.max_history` | `0` | Max historical messages per request. `0` = no limit |

## Storage

```
~/.wechatbox/
  config.yaml                          # User configuration
  data/
    wechatbox.db                       # SQLite: accounts, sessions, sync cursors
    sessions/{userId}/{sessionId}.jsonl # Conversation history (OpenAI batch format)
```

## Tech Stack

Go 1.22+, SQLite, YAML. Single binary, minimal dependencies.
