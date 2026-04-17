#!/usr/bin/env bash
# run-llama-server.sh — Start llama.cpp server with the LFM2.5 model
set -euo pipefail

# --- Configuration ---
LLAMA_BIN="/opt/llama.cpp/build/bin/llama-server"
MODEL_PATH="/opt/llama.cpp/models/lfm2.5-1.2b-instruct-q4_K_M.gguf"

HOST="127.0.0.1"
PORT="8080"
CTX_SIZE="1024"
BATCH_SIZE="32"
THREADS="2"
GPU_LAYERS="0"

# --- Launch ---
echo "Starting llama.cpp server..."
echo "  Model:   $MODEL_PATH"
echo "  Listen:  $HOST:$PORT"
echo "  Context: $CTX_SIZE | Batch: $BATCH_SIZE | Threads: $THREADS | GPU: $GPU_LAYERS"

exec "$LLAMA_BIN" \
    --host "$HOST" \
    --port "$PORT" \
    --model "$MODEL_PATH" \
    --ctx-size "$CTX_SIZE" \
    --batch-size "$BATCH_SIZE" \
    --threads "$THREADS" \
    --n-gpu-layers "$GPU_LAYERS"
