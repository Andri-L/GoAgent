package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"goagent/config"
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

// VADState represents the current state of the voice activity detector.
type VADState int

const (
	VADIdle VADState = iota
	VADSpeech
	VADSilence
)

// VoiceManager handles voice activity detection, speech accumulation,
// and orchestration of ASR + LLM processing.
type VoiceManager struct {
	cfg            config.Config
	state          VADState
	accumulator    []byte
	silenceCounter int
	speechBatches  int // consecutive batches above threshold while in Speech
	asr            *ASRClient
	agent          *Agent
	sessionID      string
	segmentCh      chan []byte
	mu             sync.Mutex
}

// NewVoiceManager creates a voice pipeline for the given agent and config.
// It spawns a background worker that processes speech segments sequentially.
func NewVoiceManager(cfg config.Config, ag *Agent) *VoiceManager {
	vm := &VoiceManager{
		cfg:       cfg,
		state:     VADIdle,
		asr:       NewASRClient(cfg.HFModelURL, cfg.HFToken),
		agent:     ag,
		sessionID: cfg.VoiceSessionID,
		segmentCh: make(chan []byte, 4),
	}
	go vm.worker()
	return vm
}

// ProcessBatch receives a single raw PCM batch from the WebSocket.
// It is non-blocking and returns immediately.
func (vm *VoiceManager) ProcessBatch(data []byte) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	db := vm.calculateDB(data)

	switch vm.state {
	case VADIdle:
		if db > vm.cfg.VADThresholdDB {
			vm.state = VADSpeech
			vm.speechBatches = 1
			vm.accumulator = append(vm.accumulator[:0], data...)
			log.Printf("[voice] Speech started (%.1f dBFS)", db)
		}
		// else: discard silence

	case VADSpeech:
		vm.accumulator = append(vm.accumulator, data...)
		vm.speechBatches++
		if db <= vm.cfg.VADThresholdDB {
			// Only allow silence transition if enough speech has been captured
			if vm.speechBatches >= vm.cfg.VADMinSpeechBatches {
				vm.state = VADSilence
				vm.silenceCounter = 0
				log.Printf("[voice] Speech→Silence (%.1f dBFS, speechBatches=%d)", db, vm.speechBatches)
			}
		}
		vm.checkMaxDuration()

	case VADSilence:
		vm.accumulator = append(vm.accumulator, data...)
		if db > vm.cfg.VADThresholdDB {
			vm.state = VADSpeech
			vm.silenceCounter = 0
			log.Printf("[voice] Silence→Speech (%.1f dBFS)", db)
		} else {
			vm.silenceCounter++
			if vm.silenceCounter >= vm.cfg.VADSilenceBatches {
				vm.emitSegment()
				vm.state = VADIdle
				vm.silenceCounter = 0
				vm.speechBatches = 0
			}
		}
		vm.checkMaxDuration()
	}
}

// calculateDB computes the RMS energy of the batch and converts it to dBFS.
func (vm *VoiceManager) calculateDB(data []byte) float64 {
	if len(data) == 0 {
		return -120.0
	}

	// data is int16 little-endian samples
	sampleCount := len(data) / 2
	if sampleCount == 0 {
		return -120.0
	}

	var sum float64
	for i := 0; i < sampleCount; i++ {
		val := int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
		sample := float64(val)
		sum += sample * sample
	}

	rms := math.Sqrt(sum / float64(sampleCount))
	if rms == 0 {
		return -120.0
	}

	db := 20.0 * math.Log10(rms/32768.0)
	return db
}

// checkMaxDuration forces a segment emit if the accumulated audio exceeds the max.
func (vm *VoiceManager) checkMaxDuration() {
	maxBytes := vm.cfg.MaxVoiceAudioSec * 16000 * 2 // 16kHz, 16-bit, mono
	if len(vm.accumulator) >= maxBytes {
		log.Printf("[voice] Max audio duration reached (%d sec), forcing segment emit", vm.cfg.MaxVoiceAudioSec)
		vm.emitSegment()
		vm.state = VADIdle
	}
}

// emitSegment sends the accumulated PCM to the worker channel.
func (vm *VoiceManager) emitSegment() {
	if len(vm.accumulator) == 0 {
		return
	}
	// Copy the slice so we can clear the accumulator immediately
	segment := make([]byte, len(vm.accumulator))
	copy(segment, vm.accumulator)
	vm.accumulator = vm.accumulator[:0]

	select {
	case vm.segmentCh <- segment:
		log.Printf("[voice] Segment emitted (%d bytes)", len(segment))
	default:
		log.Printf("[voice] Segment dropped — worker channel full")
	}
}

// worker runs in a dedicated goroutine and processes speech segments sequentially.
func (vm *VoiceManager) worker() {
	for pcm := range vm.segmentCh {
		wav := vm.buildWav(pcm)
		log.Printf("[voice] WAV built (%d bytes)", len(wav))

		text, err := vm.asr.Transcribe(wav)
		if err != nil {
			log.Printf("[voice] ASR error: %v", err)
			continue
		}
		log.Printf("[voice] Transcription: %s", text)

		// Filter noise hallucinations before sending to LLM
		if vm.isNoise(text) {
			log.Printf("[voice] Dropped noise segment: %q", text)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		answer, err := vm.agent.Run(ctx, vm.sessionID, text)
		cancel()
		if err != nil {
			log.Printf("[voice] Agent error: %v", err)
			continue
		}
		log.Printf("[voice] Agent response: %s", answer)

		// Future: send answer to TTS module
	}
}

// isNoise returns true for common ASR hallucinations on non-speech audio.
func (vm *VoiceManager) isNoise(text string) bool {
	t := strings.TrimSpace(strings.ToLower(text))
	if len(t) < 2 {
		return true
	}
	noiseWords := []string{"you", "uh", "um", "hmm", "mm", "mhm", "ah", "oh", "eh", "ha", "he", "hm"}
	for _, w := range noiseWords {
		if t == w {
			return true
		}
	}
	return false
}

// buildWav wraps raw PCM bytes in a standard RIFF/WAVE header.
func (vm *VoiceManager) buildWav(pcm []byte) []byte {
	const sampleRate = 16000
	const numChannels = 1
	const bitsPerSample = 16

	pcmLength := len(pcm)
	headerSize := 44
	totalSize := headerSize + pcmLength

	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8

	wav := bytes.NewBuffer(make([]byte, 0, totalSize))

	// RIFF header
	wav.WriteString("RIFF")
	binary.Write(wav, binary.LittleEndian, uint32(36+pcmLength))
	wav.WriteString("WAVE")

	// fmt chunk
	wav.WriteString("fmt ")
	binary.Write(wav, binary.LittleEndian, uint32(16))
	binary.Write(wav, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(wav, binary.LittleEndian, uint16(numChannels))
	binary.Write(wav, binary.LittleEndian, uint32(sampleRate))
	binary.Write(wav, binary.LittleEndian, uint32(byteRate))
	binary.Write(wav, binary.LittleEndian, uint16(blockAlign))
	binary.Write(wav, binary.LittleEndian, uint16(bitsPerSample))

	// data chunk
	wav.WriteString("data")
	binary.Write(wav, binary.LittleEndian, uint32(pcmLength))
	wav.Write(pcm)

	return wav.Bytes()
}
