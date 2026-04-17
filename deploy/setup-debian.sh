#!/usr/bin/env bash
# setup-debian.sh — Full VPS bootstrap for GoAgent + llama.cpp
# Run as root on a fresh Debian 13 server
set -euo pipefail

echo "=== GoAgent VPS Setup ==="

# --- 1. Install dependencies ---
echo "[1/5] Installing dependencies..."
apt-get update
apt-get install -y build-essential cmake git wget curl

# --- 2. Create swap (2 GB) ---
echo "[2/5] Setting up 2GB swap..."
if [ ! -f /swapfile ]; then
    fallocate -l 2G /swapfile
    chmod 600 /swapfile
    mkswap /swapfile
    swapon /swapfile
    echo '/swapfile none swap sw 0 0' >> /etc/fstab
    echo "Swap created."
else
    echo "Swap already exists, skipping."
fi

# --- 3. Clone & compile llama.cpp ---
echo "[3/5] Building llama.cpp..."
if [ ! -d /opt/llama.cpp ]; then
    git clone https://github.com/ggerganov/llama.cpp.git /opt/llama.cpp
fi
cd /opt/llama.cpp
cmake -B build -DGGML_CUDA=OFF
cmake --build build --config Release -j$(nproc)
echo "llama.cpp built at /opt/llama.cpp/build/bin/llama-server"

# --- 4. Download model ---
echo "[4/5] Downloading model..."
mkdir -p /opt/llama.cpp/models
MODEL_PATH="/opt/llama.cpp/models/lfm2.5-1.2b-instruct-q4_K_M.gguf"
if [ ! -f "$MODEL_PATH" ]; then
    wget -O "$MODEL_PATH" \
        "https://huggingface.co/LiquidAI/LFM2-1.2B-Instruct-GGUF/resolve/main/LFM2-1.2B-Instruct-Q4_K_M.gguf"
    echo "Model downloaded."
else
    echo "Model already exists, skipping."
fi

# --- 5. Create systemd services ---
echo "[5/5] Creating systemd services..."

# llama-server service
cat > /etc/systemd/system/llama-server.service <<EOF
[Unit]
Description=llama.cpp Server
After=network.target

[Service]
Type=simple
ExecStart=/opt/llama.cpp/deploy/run-llama-server.sh
WorkingDirectory=/opt/llama.cpp
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# goagent service
cat > /etc/systemd/system/goagent.service <<EOF
[Unit]
Description=GoAgent Server
After=llama-server.service
Requires=llama-server.service

[Service]
Type=simple
ExecStart=/opt/goagent/goagent-linux
WorkingDirectory=/opt/goagent
Environment=LLM_BASE_URL=http://127.0.0.1:8080/v1
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable llama-server goagent

echo ""
echo "=== Setup complete! ==="
echo ""
echo "Next steps:"
echo "  1. Copy the run-llama-server.sh to /opt/llama.cpp/deploy/"
echo "  2. Copy the goagent-linux binary to /opt/goagent/"
echo "  3. Start services:"
echo "     systemctl start llama-server"
echo "     systemctl start goagent"
echo "  4. Test:"
echo "     curl http://localhost:8081/health"
