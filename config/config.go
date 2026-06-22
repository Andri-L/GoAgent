package config

import (
	"fmt"
	"os"
	"strconv"
)

// config holds all application settings
type Config struct {
	LLMBaseURL        string
	ModelName         string
	MaxTokens         int
	Temperature       float64
	MaxIterations     int
	SystemPrompt      string
	ListenAddr        string
	LLMAPIKey         string
	HFToken           string
	HFModelURL        string
	VADThresholdDB    float64
	VADSilenceBatches int
	MaxVoiceAudioSec  int
	VoiceAgentToken   string
	VoiceSessionID    string
}

// Load reads configuration from environment variables, falling back to defaults.
func Load() Config {
	return Config{
		LLMBaseURL:        getEnv("LLM_BASE_URL", "http://127.0.0.1:8082/v1"),
		ModelName:         getEnv("MODEL_NAME", "lfm2.5-1.2b"),
		MaxTokens:         getEnvInt("MAX_TOKENS", 256),
		Temperature:       getEnvFloat("TEMPERATURE", 0.1),
		MaxIterations:     getEnvInt("MAX_ITERATIONS", 5),
		SystemPrompt:      getEnv("SYSTEM_PROMPT", defaultSystemPrompt),
		ListenAddr:        getEnv("LISTEN_ADDR", ":8080"),
		LLMAPIKey:         getEnv("LLM_API_KEY", ""),
		HFToken:           getEnv("HF_TOKEN", ""),
		HFModelURL:        getEnv("HF_MODEL_URL", "https://router.huggingface.co/hf-inference/models/openai/whisper-large-v3-turbo"),
		VADThresholdDB:    getEnvFloat("VAD_THRESHOLD_DB", -40.0),
		VADSilenceBatches: getEnvInt("VAD_SILENCE_BATCHES", 20),
		MaxVoiceAudioSec:  getEnvInt("MAX_VOICE_AUDIO_SEC", 30),
		VoiceAgentToken:   getEnv("VOICE_AGENT_TOKEN", ""),
		VoiceSessionID:    getEnv("VOICE_SESSION_ID", "voice"),
	}
}

// String returns a human-readable representation of the config.
func (c Config) String() string {
	keyHint := ""
	if c.LLMAPIKey != "" {
		keyHint = " LLMAPIKey=***"
	}
	return fmt.Sprintf(
		"LLMBaseURL=%s ModelName=%s MaxTokens=%d Temperature=%.2f MaxIterations=%d ListenAddr=%s%s VADThresholdDB=%.1f VADSilenceBatches=%d MaxVoiceAudioSec=%d VoiceSessionID=%s",
		c.LLMBaseURL, c.ModelName, c.MaxTokens, c.Temperature, c.MaxIterations, c.ListenAddr, keyHint,
		c.VADThresholdDB, c.VADSilenceBatches, c.MaxVoiceAudioSec, c.VoiceSessionID,
	)
}

// --- helper functions ---

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

const defaultSystemPrompt = `You are Andri2, a conversational and relaxed AI assistant in a Discord server.
Speak naturally and casually in flowing paragraphs, just like you are chatting with a friend.

CRITICAL RULES FOR YOUR FORMATTING:
- DO NOT use bulleted lists, numbered lists, or heavy markdown. 
- DO NOT use formal transitions like "Firstly", "Secondly", or "In summary".
- Instead of listing steps, weave instructions and explanations together into natural prose.

When you need to gather information, use your available tools and blend the findings smoothly into your casual response.`
