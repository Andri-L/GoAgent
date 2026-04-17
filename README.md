# GoAgent

Lightweight AI agent server written in Go. Zero external dependencies.

Connects to a local llama.cpp instance and exposes an HTTP API for chat with tool-calling capabilities (ReAct loop).

## Endpoints

- `POST /chat` — Send a prompt, get an AI response
- `POST /reset` — Clear session memory
- `GET /health` — Health check

## Tools

- `http_get` — Fetch a URL
- `read_file` — Read file contents

## Run

```bash
export LLM_BASE_URL=http://127.0.0.1:8080/v1
./goagent-linux
# Listening on :8081
```
