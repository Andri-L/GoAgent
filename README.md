# GoAgent

Lightweight AI agent server written in Go. Connects to any OpenAI-compatible LLM API (local llama.cpp, Groq, OpenAI, etc.) and exposes an HTTP API + WebSocket for voice-enabled chat with tool-calling capabilities (ReAct loop).

## Features

- **Text Chat** — `POST /chat` with session memory
- **Voice Input** — WebSocket `/audio` receives raw PCM from ESP32
- **Voice Activity Detection (VAD)** — Detects speech start/stop automatically
- **Speech Recognition (ASR)** — Sends audio to Hugging Face Whisper for transcription
- **Tools** — `http_get`, `read_file` (ReAct loop)
- **Zero external Go dependencies** (except `gorilla/websocket` for voice)

## Architecture

```
ESP32 (INMP441 mic) ──ws binary──▶ GoAgent :8080 /audio
                                         │
                                         ▼
                              VoiceManager (VAD)
                                         │
                                         ▼
                              buildWav ──▶ ASR (HF Whisper)
                                         │
                                         ▼
                              Agent.Run(sessionID, text)
                                         │
                                         ▼
                              Text response (logged / future: TTS)
```

## Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/chat` | POST | Send a prompt, get AI response |
| `/reset` | POST | Clear session memory |
| `/health` | GET | Health check |
| `/audio` | WebSocket | ESP32 raw PCM audio ingestion |

## Tools

- `http_get` — Fetch a URL
- `read_file` — Read file contents

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LLM_BASE_URL` | No | `http://127.0.0.1:8082/v1` | LLM server URL |
| `MODEL_NAME` | No | `lfm2.5-1.2b` | Model name sent to the LLM |
| `LLM_API_KEY` | No | — | API key for hosted providers (Groq, OpenAI, etc.). Leave empty for local llama.cpp. |
| `LISTEN_ADDR` | No | `:8080` | HTTP + WebSocket port |
| `HF_TOKEN` | **Yes** | — | Hugging Face API token |
| `VOICE_AGENT_TOKEN` | **Yes** | — | Must match ESP32 auth token |
| `VAD_THRESHOLD_DB` | No | `-40.0` | dBFS silence threshold |
| `VAD_SILENCE_BATCHES` | No | `20` | Batches to confirm speech end |

## Switching LLM Provider

GoAgent uses a provider-agnostic OpenAI-compatible client. To switch backends, just change the URL, model, and API key:

### Local llama.cpp (default)

```bash
LLM_BASE_URL=http://127.0.0.1:8082/v1
MODEL_NAME=lfm2.5-1.2b
LLM_API_KEY=          # leave empty
```

### Groq

```bash
LLM_BASE_URL=https://api.groq.com/openai/v1
MODEL_NAME=llama-3.3-70b-versatile
LLM_API_KEY=gsk_your_groq_key
```

Any other OpenAI-compatible API works the same way.

## Run

```bash
# Copy .env.example to .env and fill in your tokens
cp .env.example .env
# Edit .env with your LLM provider, HF_TOKEN, and VOICE_AGENT_TOKEN

# For local llama.cpp: start it on port 8082
./llama-server --port 8082 ...

# Start GoAgent
source .env
./goagent-linux
# Listening on :8080
```

## Voice Pipeline

1. ESP32 sends 16-bit PCM 16kHz mono batches (~64ms each) via WebSocket
2. GoAgent calculates RMS → dBFS per batch
3. VAD state machine: `Idle → Speech → Silence → Idle`
4. Speech segment assembled into WAV in memory
5. WAV sent to Hugging Face Whisper (`openai/whisper-large-v3-turbo`)
6. Transcription fed to Agent ReAct loop
7. Agent response logged (future: TTS + audio return to ESP32)

## Deployment

See `deploy/setup-debian.sh` for full VPS bootstrap.

```bash
# On VPS
sudo systemctl start llama-server   # port 8082
sudo systemctl start goagent        # port 8080
```

## Security Notes

- **Never commit `.env` files** — tokens are sensitive
- Rotate `HF_TOKEN` and `VOICE_AGENT_TOKEN` before production
- The ESP32 auth token is hardcoded in firmware — keep it secret

## License

Apache License 2.0
