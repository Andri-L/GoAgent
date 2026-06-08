# GoAgent Voice Integration — Engineering Plan

## Status: Approved for Implementation

---

## 1. Executive Summary

This plan integrates real-time voice input (and later, voice output) into the GoAgent server. The ESP32 Audio Batch Processor will stream raw PCM audio to GoAgent via WebSocket. GoAgent will perform Voice Activity Detection (VAD), assemble speech segments into WAV files, send them to Hugging Face Whisper for transcription, and feed the resulting text into the existing ReAct agent loop.

**Key design decision:** The WebSocket server moves from Node.js into GoAgent. In production, a single Go binary (`goagent-linux`) runs everything on port 8080. The Node.js `audio-websocket` server remains on the developer's PC for debugging and monitoring.

---

## 2. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                        BMO (Physical Device)                          │
│  ┌────────────┐   ┌────────────┐   ┌────────────┐                   │
│  │  INMP441   │   │   ESP32    │   │ MAX98357A  │                   │
│  │  (Micro)   │──▶│ (Capture + │──▶│  (Speaker) │                   │
│  │            │   │ WebSocket) │   │            │                   │
│  └────────────┘   └────────────┘   └────────────┘                   │
└─────────────────────────────────────────────────────────────────────┘
                              │ ws://:8080/audio (binary PCM)
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      GoAgent (OCI Free VPS)                         │
│                                                                     │
│  ┌──────────────────┐                                               │
│  │  WebSocket /audio │◄── ESP32 binary batches                       │
│  │   (gorilla/websocket)                                            │
│  └──────────────────┘                                               │
│           │                                                         │
│           ▼                                                         │
│  ┌──────────────────┐    ┌──────────────────┐                      │
│  │  VoiceManager    │───▶│  ASRClient (HF)   │                      │
│  │   VAD State Machine│   │  Whisper Turbo   │                      │
│  │   Accumulate WAV │    └──────────────────┘                      │
│  └──────────────────┘         │ transcription text                  │
│           │                   ▼                                     │
│           │            ┌──────────────────┐                        │
│           │            │  Agent.Run()      │                        │
│           │            │  ReAct Loop       │                        │
│           │            │  Session "voice"  │                        │
│           │            └──────────────────┘                        │
│           │                   │ text response                     │
│           │                   ▼                                     │
│           │            (Logged / future: TTS)                     │
│  └─────────────────────────────────────────────────────────────────────┘
                              │ ws://:8080/audio (same protocol)
                              │
┌─────────────────────────────────────────────────────────────────────┐
│                    Developer PC (Debug Only)                        │
│  ┌──────────────────┐                                              │
│  │  audio-websocket │                                              │
│  │  (Node.js, 8080) │  Monitor dashboard, rolling buffer export    │
│  └──────────────────┘                                              │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 3. Audio Format Contract

The ESP32 and GoAgent must agree on the audio format. This is already established by the ESP32 firmware:

| Parameter | Value |
|-----------|-------|
| **Sample Rate** | 16,000 Hz |
| **Bit Depth** | 16-bit signed integer |
| **Channels** | Mono (1) |
| **Byte Order** | Little-endian |
| **Codec** | Raw PCM (uncompressed) |
| **Batch Size** | 1024 samples = 2048 bytes |
| **Batch Duration** | ~64 ms |
| **Frame Rate** | ~16 batches/second |

**WebSocket Protocol:**
- `Connection: Upgrade` to WebSocket
- Path: `/audio`
- Message type: Binary frames (`opcode=0x02`)
- Payload: raw PCM bytes (no headers, no JSON)
- Authentication: `Authorization: Bearer <token>` in the HTTP Upgrade request

---

## 4. Phase 1: Dependency & Port Resolution

### 4.1 Add WebSocket Dependency

The Go standard library does not include WebSocket framing. We use the industry-standard library:

```
github.com/gorilla/websocket v1.5.3
```

This is the only new external dependency. It adds ~200KB to the binary.

### 4.2 Resolve Port 8080 Conflict

The ESP32 hardcodes port `8080` in its WebSocket connection. The current `llama.cpp` also uses `8080`. We must move one:

| Service | Old Port | New Port | Notes |
|---------|----------|----------|-------|
| GoAgent (HTTP + WebSocket) | 8081 | **8080** | ESP32 connects here |
| `llama.cpp` | 8080 | **8082** | Local only, no external access |

**Files to modify:**
- `deploy/run-llama-server.sh`: `PORT="8080"` → `PORT="8082"`
- `config/config.go`: `LLM_BASE_URL` default → `http://127.0.0.1:8082/v1`
- `deploy/setup-debian.sh`: `llama-server` systemd unit references
- `.github/workflows/deploy.yml`: If any env vars reference the port

---

## 5. Phase 2: WebSocket Endpoint in GoAgent

### 5.1 New Endpoint: `GET /audio` (WebSocket Upgrade)

In `main.go`, add a handler for path `/audio`:

1. **Validate the HTTP Upgrade request** before upgrading:
   - Check `Authorization: Bearer <token>` against `cfg.VoiceAgentToken`.
   - Reject with `401 Unauthorized` if the token doesn't match.
   - This matches the ESP32's hardcoded token.

2. **Upgrade to WebSocket** using `websocket.Upgrader`.
   - `ReadBufferSize: 2048`, `WriteBufferSize: 2048` (or larger).
   - No origin check needed (the ESP32 is not a browser).

3. **Spawn a goroutine per connection** that:
   - Reads `websocket.BinaryMessage` in a loop.
   - For each frame: call `voiceManager.ProcessBatch(data)`.
   - `ProcessBatch` is **non-blocking** (returns immediately, voice is buffered internally).
   - If the connection drops, log it and exit the goroutine.

4. **Support multiple concurrent connections** (but warn if more than one).
   - The ESP32 connects to a single WebSocket.
   - If multiple ESP32s connect, their audio is interleaved. This is acceptable for Phase 1 (single BMO). A per-connection VoiceManager can be added later.

### 5.2 ESP32 Protocol Compatibility

The ESP32 sends raw PCM in **binary WebSocket frames**. We do NOT need to handle:
- Text frames (the ESP32 never sends them)
- Ping/pong (the ESP32 never sends them)
- Close frames (handled by the library)

The WebSocket library (`gorilla/websocket`) abstracts all of this.

---

## 6. Phase 3: Voice Manager (VAD)

### 6.1 New File: `agent/voice.go`

**Structs:**

```go
type VADState int

const (
    VADIdle VADState = iota
    VADSpeech
    VADSilence
)

type VoiceManager struct {
    cfg             config.Config
    state           VADState
    accumulator     []byte
    silenceCounter  int
    asr             *ASRClient
    agent           *Agent
    sessionID       string
    segmentCh       chan []byte
    mu              sync.Mutex
}
```

### 6.2 VAD Algorithm

**Energy Calculation per Batch:**

1. Decode the 2048 bytes as `int16` little-endian samples (1024 samples).
2. Compute **RMS**: `sqrt(sum(sample^2) / n)`
3. Convert to **dBFS**: `20 * log10(rms / 32768.0)`
4. If `dBFS` is `-inf` (all zeros), treat as `-120` dBFS.

**State Machine:**

| State | dBFS > Threshold | dBFS <= Threshold | Action |
|-------|------------------|-------------------|--------|
| **Idle** | Transition to `Speech` | Stay in `Idle` | Discard batch. |
| **Speech** | Stay in `Speech` | Transition to `Silence`, `silenceCounter = 0` | Append batch. |
| **Silence** | Transition to `Speech` | Increment `silenceCounter`. If `silenceCounter >= cfg.VADSilenceBatches`: transition to `Idle`, emit segment. | Append batch (part of the utterance). |

**Why append in `Silence`?** The trailing silence is part of the speech segment. Whisper needs a natural audio boundary to stop transcribing. If we cut the audio immediately at the first silent batch, the WAV ends abruptly and Whisper may truncate words.

**Default Thresholds:**

| Parameter | Default Value | Rationale |
|-----------|---------------|-----------|
| `VAD_THRESHOLD_DB` | `-40.0` | Below this: silence/ambient noise. Above this: voice. |
| `VAD_SILENCE_BATCHES` | `20` | ~1.28 seconds of silence to confirm the user stopped talking. |
| `MAX_VOICE_AUDIO_SEC` | `30` | Maximum 30 seconds of audio. If exceeded, force emit segment. |

**Why `20` silence batches?** At 16 batches/second, 20 batches = 1.28 seconds. This is a common VAD value (0.5-2s). It prevents cutting the user off during short pauses between words.

### 6.3 `emitSegment` (Building the WAV)

When speech ends, the accumulated PCM bytes must be wrapped in a **RIFF/WAVE header** so the Hugging Face API recognizes it as a WAV file.

```go
func buildWav(pcm []byte, sampleRate, numChannels, bitsPerSample int) []byte {
    // 44-byte header + pcm data
    // RIFF, WAVE, fmt, data chunks
}
```

The WAV is built entirely in memory (no disk writes).

---

## 7. Phase 4: ASR Client (Hugging Face Whisper)

### 7.1 New File: `agent/asr.go`

**ASRClient:**

```go
type ASRClient struct {
    url         string
    token       string
    maxRetries  int
    client      *http.Client
}
```

**`Transcribe(wav []byte) (string, error)`:**

1. Build HTTP POST:
   - `url`: `https://router.huggingface.co/hf-inference/models/openai/whisper-large-v3-turbo`
   - Headers: `Authorization: Bearer <token>`, `Content-Type: audio/wav`
   - Body: the WAV bytes

2. **Handle HTTP responses:**
   - `200 OK`: Parse JSON, extract `text` field (e.g., `result["text"]`).
   - `503 Service Unavailable`: The model is cold-starting. Parse the JSON error body for `estimated_time`. Sleep for that duration, then retry (up to `maxRetries`).
   - `429 Too Many Requests`: Rate limit hit. Return error.
   - Other errors: Return generic error.

3. **Timeout:** Use `http.Client` with a 60-second timeout.

**Security:** The Hugging Face token must be provided via the `HF_TOKEN` environment variable. **No hardcoded fallback.** The server must refuse to start if the token is missing.

---

## 8. Phase 5: Segment Worker

### 8.1 Sequential Processing

Voice transcription must be **serialized** per session. If the user speaks two utterances back-to-back, the second must wait for the first to finish (ASR + LLM). This prevents race conditions on the conversation history.

**Implementation:**

```go
func (vm *VoiceManager) worker() {
    for pcm := range vm.segmentCh {
        wav := buildWav(pcm, 16000, 1, 16)
        text, err := vm.asr.Transcribe(wav)
        if err != nil {
            log.Printf("[voice] ASR error: %v", err)
            continue
        }
        log.Printf("[voice] Transcription: %s", text)
        
        answer, err := vm.agent.Run(context.Background(), vm.sessionID, text)
        if err != nil {
            log.Printf("[voice] Agent error: %v", err)
            continue
        }
        log.Printf("[voice] Agent response: %s", answer)
        
        // Future: send answer to TTS module
    }
}
```

The worker runs in a **dedicated goroutine** spawned when `VoiceManager` is created.

---

## 9. Phase 6: Configuration

### 9.1 New Environment Variables

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `HF_TOKEN` | *(none)* | **Yes** | Hugging Face API token for Whisper. |
| `HF_MODEL_URL` | `https://router.huggingface.co/hf-inference/models/openai/whisper-large-v3-turbo` | No | Whisper endpoint. |
| `VAD_THRESHOLD_DB` | `-40.0` | No | dBFS threshold. Below = silence. |
| `VAD_SILENCE_BATCHES` | `20` | No | Batches of silence before speech ends. |
| `MAX_VOICE_AUDIO_SEC` | `30` | No | Maximum audio length before forcing segment emit. |
| `VOICE_AGENT_TOKEN` | *(none)* | **Yes** | Must match the ESP32 hardcoded token. |
| `VOICE_SESSION_ID` | `voice` | No | Session ID for the voice conversation. |
| `LLM_BASE_URL` | `http://127.0.0.1:8082/v1` | No | Updated for port 8082. |

---

## 10. Phase 7: Deployment & ESP32

### 10.1 Server Changes

1. **`setup-debian.sh`:**
   - Remove Node.js installation (no longer needed for production).
   - Update `llama-server` port to 8082.
   - Add `HF_TOKEN` and `VOICE_AGENT_TOKEN` to the `goagent` systemd unit environment variables.
   - Set `LISTEN_ADDR=:8080` for the GoAgent.

2. **`run-llama-server.sh`:**
   - Change `PORT="8080"` to `PORT="8082"`.

3. **`deploy.yml` (GitHub Actions):**
   - Verify the build still works with the new dependency.
   - Ensure `go mod tidy` is run before build.

### 10.2 ESP32 Changes

1. **`audio_batch_proc.ino`:**
   - Change `WS_FALLBACK_URL` from `ws://192.168.80.15:8080/audio` to `ws://<oci-public-ip>:8080/audio`.
   - The `WS_SERVER_HOST` (`audio-webserver.local`) and mDNS logic can stay, but the fallback must be the OCI IP.
   - The `AUTH_TOKEN` stays the same. It must match `VOICE_AGENT_TOKEN` on the server.

2. **WiFi Credentials:**
   - The ESP32 currently connects to `LINA SOFI 5G`. When deployed, it may need to connect to a different network (e.g., the user's phone hotspot or the OCI server's network). This is a deployment detail, not an architecture change.

### 10.3 OCI Firewall

- Open port **8080** (TCP) for the WebSocket. The ESP32 connects from the internet.
- Port **8081** is no longer needed externally.
- Port **8082** is local only (llama.cpp), no firewall rule needed.

---

## 11. Phase 8: TTS + Audio Return (Planning Only)

This section is **not implemented yet**. It defines the future architecture for sending the agent's response back to the BMO as audio.

### 11.1 TTS Engine Options

| Option | Pros | Cons |
|--------|------|------|
| **Piper** (local, CPU) | Free, fast, no network, runs on VPS | Robotic quality, limited voices |
| **Hugging Face TTS** (cloud) | Good quality, no local setup | Cold starts, rate limits, latency |
| **Cloud APIs** (Google, Azure, ElevenLabs) | Excellent quality | Costs money |

**Recommendation:** Start with **Piper** for a self-contained, zero-cost deployment. Upgrade to a cloud API if voice quality is insufficient.

### 11.2 Audio Return Architecture

**Data Flow:**

```
Agent Text Response ──▶ Piper TTS ──▶ PCM/WAV ──▶ GoAgent Response Buffer
                                              │
                                              ▼
                                    ESP32 connects to /response (WebSocket)
                                              │
                                              ▼
                                    I2S TX ──▶ MAX98357A ──▶ Speaker
```

**Transport Options:**

| Option | Pros | Cons |
|--------|------|------|
| **WebSocket `/response`** | Real-time, streaming, low latency | Requires ESP32 WebSocket client (already exists) |
| **HTTP Polling** `GET /voice/response` | Simpler ESP32 code, no persistent connection | Latency, overhead, not real-time |

**Recommendation:** Use a **WebSocket** on the same server (port 8080, path `/response`). The ESP32 already has WebSocket code. It can open a second connection for receiving audio.

### 11.3 ESP32 Playback Architecture

The ESP32 currently has Core 0 "free" for "future audio reception." This is exactly what we need.

1. **Add I2S TX driver** for the MAX98357A speaker.
   - Share the same I2S peripheral as the microphone (WS=25, SCK=26) or use a second I2S peripheral.
   - The MAX98357A uses I2S input to drive the speaker.

2. **Add a WebSocket client** for the response endpoint.
   - Reuse the existing `websocket.cpp` logic.
   - Connect to `ws://<oci-ip>:8080/response`.
   - Receive binary audio frames.

3. **Add a playback task** on Core 0.
   - Queue received audio frames.
   - Feed them to `i2s_write()` in the correct format (16-bit PCM, 16kHz, mono).
   - The MAX98357A DAC converts the digital stream to analog audio.

### 11.4 GoAgent Response Buffer

- A `sync.Map` or `map[string]*responseBuffer` keyed by `session_id`.
- The `responseBuffer` holds the latest audio segments (or a full WAV file).
- When a `/response` WebSocket connects, the server sends the pending audio.
- After playback, the buffer is cleared.

### 11.5 Session State Machine (Voice)

```
Idle ──(speech detected)──▶ Listening
Listening ──(silence)──▶ Processing
Processing ──(ASR + LLM + TTS)──▶ Speaking
Speaking ──(audio sent)──▶ Idle
```

The BMO must not listen while it is speaking (or handle echo cancellation). This is a future enhancement.

---

## 12. Implementation Checklist

### Code
- [ ] Add `gorilla/websocket` to `go.mod`
- [ ] Move `llama.cpp` to port 8082 (`run-llama-server.sh`, `config.go`)
- [ ] Add WebSocket endpoint `/audio` in `main.go`
- [ ] Create `agent/voice.go` (VAD state machine, WAV builder)
- [ ] Create `agent/asr.go` (HF Whisper client)
- [ ] Add voice worker goroutine in `VoiceManager`
- [ ] Update `config/config.go` with new voice settings
- [ ] Add `HF_TOKEN` and `VOICE_AGENT_TOKEN` as required env vars

### Deployment
- [ ] Update `setup-debian.sh` (llama port, env vars, remove Node.js)
- [ ] Update `.github/workflows/deploy.yml` (build with new dep)
- [ ] Update ESP32 `audio_batch_proc.ino` (OCI IP, token match)
- [ ] Test build on Windows: `go build`
- [ ] Test build for Linux: `GOOS=linux GOARCH=amd64 go build`

### Testing
- [ ] Start llama.cpp on port 8082
- [ ] Start GoAgent on port 8080
- [ ] Connect ESP32, verify WebSocket handshake
- [ ] Speak into the BMO, verify VAD detects speech start/stop
- [ ] Verify WAV is built and sent to HF
- [ ] Verify transcription is received
- [ ] Verify agent responds with text
- [ ] Check conversation memory persists across utterances

### Future (TTS)
- [ ] Install Piper TTS on the VPS
- [ ] Add `/response` WebSocket endpoint in GoAgent
- [ ] Add ESP32 I2S TX + playback task
- [ ] Test end-to-end voice conversation

---

## 13. Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Hugging Face 503 cold start | First utterance delayed 20-40s | Implement retry with `estimated_time`. Pre-warm the model by sending a silent WAV on startup. |
| HF rate limit (429) | Free tier only allows ~X requests/hour | Cache responses. Add a local fallback (e.g., `faster-whisper` on the VPS). |
| ESP32 WiFi disconnect | Audio stream drops | ESP32 auto-reconnects (already implemented). GoAgent tolerates dropped connections. |
| VAD false positives | Ambient noise triggers speech | Tune `VAD_THRESHOLD_DB`. Add a minimum speech duration filter. |
| VAD false negatives | Quiet speech cut off | Tune `VAD_SILENCE_BATCHES`. The `Silence` state accumulates trailing audio. |
| llama.cpp context overflow | Long conversations fail | The LFM2.5 model has 1024 context. Conversation history is trimmed by the LLM layer. |
| Single-threaded llama.cpp | Agent is slow with many requests | Acceptable for a single BMO. The VPS is free-tier. |

---

## 14. Notes

- **Security:** The `VOICE_AGENT_TOKEN` must match the ESP32's hardcoded token. If the token is leaked, anyone can send audio to the server. Consider rotating the token before deployment.
- **Performance:** The WebSocket handler runs in a goroutine per connection. The VAD calculation is CPU-bound but fast (1024 int16 samples = negligible). The ASR + LLM calls are I/O-bound and run in a separate worker goroutine.
- **Memory:** The `accumulator` holds at most 30 seconds of audio = 30s × 16000 samples/s × 2 bytes = 960KB. Well within the VPS's 2GB RAM.
- **Disk:** Zero disk writes for the voice pipeline. All audio is in memory.

---

## 15. Summary

This plan transforms GoAgent from a text-only chatbot into a **voice-enabled AI agent**. A single Go binary handles everything: WebSocket ingestion, VAD, speech recognition, and reasoning. The architecture is simple, has minimal dependencies, and is designed for deployment on a free VPS.

**Next step:** Begin implementation.
