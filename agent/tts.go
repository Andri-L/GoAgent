package agent

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TTSClient synthesizes speech by calling the Piper CLI binary.
type TTSClient struct {
	modelPath string
	piperBin  string
	speed     float64
}

// NewTTSClient creates a TTS client. piperBin is the path to the `piper` executable.
func NewTTSClient(piperBin, modelPath string, speed float64) *TTSClient {
	return &TTSClient{
		modelPath: modelPath,
		piperBin:  piperBin,
		speed:     speed,
	}
}

// Synthesize converts text to raw PCM 16-bit mono bytes at 16000 Hz.
// It calls the Piper CLI directly, reads the resulting WAV, and resamples if needed.
func (c *TTSClient) Synthesize(text string) ([]byte, error) {
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}

	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("piper_%d.wav", os.Getpid()))
	defer os.Remove(tmpFile)

	cmd := exec.Command(c.piperBin,
		"--model", c.modelPath,
		"--output_file", tmpFile,
		"--length_scale", fmt.Sprintf("%.2f", c.speed),
	)
	cmd.Stdin = strings.NewReader(text)
	piperDir := filepath.Dir(c.piperBin)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+piperDir)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("piper failed: %w\noutput: %s", err, string(out))
	}

	wavData, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("read wav: %w", err)
	}
	if len(wavData) == 0 {
		return nil, fmt.Errorf("piper produced empty wav")
	}

	pcm, srcRate, err := parseWav(wavData)
	if err != nil {
		return nil, fmt.Errorf("parse wav: %w", err)
	}

	// Resample to 16000 Hz if needed
	if srcRate != 16000 {
		pcm = resamplePCM16(pcm, srcRate, 16000)
	}

	return pcm, nil
}

// parseWav extracts PCM data and sample rate from a RIFF/WAVE file.
func parseWav(data []byte) (pcm []byte, sampleRate int, err error) {
	if len(data) < 44 {
		return nil, 0, fmt.Errorf("wav too short (%d bytes)", len(data))
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("invalid riff header")
	}
	if string(data[12:16]) != "fmt " {
		return nil, 0, fmt.Errorf("missing fmt chunk")
	}
	sampleRate = int(binary.LittleEndian.Uint32(data[24:28]))

	// Walk chunks to find "data"
	offset := 36
	for offset < len(data)-8 {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if chunkID == "data" {
			end := offset + 8 + chunkSize
			if end > len(data) {
				end = len(data)
			}
			return data[offset+8 : end], sampleRate, nil
		}
		offset += 8 + chunkSize
		if chunkSize%2 == 1 {
			offset++ // pad byte
		}
	}
	return nil, 0, fmt.Errorf("missing data chunk")
}

// resamplePCM16 resamples int16 mono PCM using linear interpolation.
func resamplePCM16(pcm []byte, srcRate, dstRate int) []byte {
	if len(pcm)%2 != 0 {
		pcm = pcm[:len(pcm)-1]
	}
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}

	ratio := float64(dstRate) / float64(srcRate)
	newLen := int(float64(len(samples)) * ratio)
	if newLen == 0 {
		return []byte{}
	}

	resampled := make([]int16, newLen)
	for i := 0; i < newLen; i++ {
		srcIdx := float64(i) / ratio
		idx := int(srcIdx)
		frac := srcIdx - float64(idx)
		if idx+1 >= len(samples) {
			resampled[i] = samples[idx]
		} else {
			val := float64(samples[idx])*(1-frac) + float64(samples[idx+1])*frac
			if val > 32767 {
				val = 32767
			} else if val < -32768 {
				val = -32768
			}
			resampled[i] = int16(val)
		}
	}

	out := make([]byte, len(resampled)*2)
	for i, s := range resampled {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}
