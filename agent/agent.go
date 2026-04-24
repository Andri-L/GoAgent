package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"goagent/config"
	"io"
	"net/http"
	"sync"
	"time"
)

// session holds a single user's conversation history.
type session struct {
	messages []chatMessage
	lastUsed time.Time
}

// Agent holds the LLM client settings and session storage.
type Agent struct {
	cfg      config.Config
	sessions map[string]*session
	mu       sync.Mutex
}

// New creates a new Agent from the given config.
func New(cfg config.Config) *Agent {
	return &Agent{
		cfg:      cfg,
		sessions: make(map[string]*session),
	}
}

// --- OpenAI-compatible request/response types ---

type chatMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatRequest struct {
	Model       string                   `json:"model"`
	Messages    []chatMessage            `json:"messages"`
	Tools       []map[string]interface{} `json:"tools,omitempty"`
	MaxTokens   int                      `json:"max_tokens"`
	Temperature float64                  `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// --- Core ReAct loop ---

// Run sends a question to the LLM and loops through tool calls until it gets a final answer.
func (a *Agent) Run(ctx context.Context, sessionID, question string) (string, error) {
	// --- 1. Lock: read/create session, copy messages, unlock ---
	a.mu.Lock()
	sess, exists := a.sessions[sessionID]
	if !exists || sessionID == "" {
		sess = &session{
			messages: []chatMessage{
				{Role: "system", Content: a.cfg.SystemPrompt},
			},
		}
		if sessionID != "" {
			a.sessions[sessionID] = sess
		}
	}
	messages := make([]chatMessage, len(sess.messages))
	copy(messages, sess.messages)
	a.mu.Unlock()

	// --- 2. Append user message to local copy ---
	messages = append(messages, chatMessage{Role: "user", Content: question})

	tools := ToolDefinitions()

	// --- 3. Run LLM loop (no lock held) ---
	for i := 0; i < a.cfg.MaxIterations; i++ {
		resp, err := a.callLLM(ctx, messages, tools)
		if err != nil {
			return "", fmt.Errorf("LLM call failed (iteration %d): %w", i, err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("LLM returned no choices")
		}
		msg := resp.Choices[0].Message

		if len(msg.ToolCalls) == 0 {
			// Final answer — append assistant reply
			messages = append(messages, msg)

			if msg.ReasoningContent != "" {
				fmt.Printf("\n[AI THINKING CHAIN]:\n%s\n\n", msg.ReasoningContent)
			}

			// --- 4. Lock: write updated messages back, unlock ---
			if sessionID != "" {
				a.mu.Lock()
				sess.messages = messages
				sess.lastUsed = time.Now()
				a.mu.Unlock()
			}
			return msg.Content, nil
		}

		messages = append(messages, msg)
		for _, tc := range msg.ToolCalls {
			var args map[string]string
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]string{}
			}
			result := ExecuteTool(tc.Function.Name, args)
			messages = append(messages, chatMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	return "", fmt.Errorf("max iterations (%d) reached without a final answer", a.cfg.MaxIterations)
}

// RunJSON wraps Run and returns the result as JSON bytes.
func (a *Agent) RunJSON(ctx context.Context, sessionID, question string) ([]byte, error) {
	answer, err := a.Run(ctx, sessionID, question)
	if err != nil {
		errResp := map[string]string{"error": err.Error()}
		return json.Marshal(errResp)
	}
	resp := map[string]string{"response": answer}
	return json.Marshal(resp)
}

// ResetSession deletes a session's history.
func (a *Agent) ResetSession(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionID)
}

// CleanOldSessions removes sessions not used since maxAge ago.
func (a *Agent) CleanOldSessions(maxAge time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, sess := range a.sessions {
		if sess.lastUsed.Before(cutoff) {
			delete(a.sessions, id)
		}
	}
}

// --- HTTP call to llama.cpp ---

func (a *Agent) callLLM(ctx context.Context, messages []chatMessage, tools []map[string]interface{}) (*chatResponse, error) {
	reqBody := chatRequest{
		Model:       a.cfg.ModelName,
		Messages:    messages,
		Tools:       tools,
		MaxTokens:   a.cfg.MaxTokens,
		Temperature: a.cfg.Temperature,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	url := a.cfg.LLMBaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http call: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(respBody))
	}
	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &chatResp, nil
}
