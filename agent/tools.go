package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const maxOutputBytes = 4096 // 4 KB cap for all tool outputs

// truncate limits a string to maxLen bytes.
// truncate prevents sending oversized text to the LLM, keeping token usage and context window in check.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (truncated)"
}

// --- Tool: shell ---
func toolShell(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return truncate(fmt.Sprintf("error: %v\noutput: %s", err, string(out)), maxOutputBytes)
	}
	return truncate(string(out), maxOutputBytes)
}

// --- Tool: http_get ---
func toolHTTPGet(url string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Sprintf("error creating request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOutputBytes))
	if err != nil {
		return fmt.Sprintf("error reading body: %v", err)
	}
	return truncate(fmt.Sprintf("status: %d\n%s", resp.StatusCode, string(body)), maxOutputBytes)
}

// --- Tool: read_file ---
func toolReadFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxOutputBytes))
	if err != nil {
		return fmt.Sprintf("error reading: %v", err)
	}
	return string(data)
}

// --- Dispatcher ---
// ExecuteTool runs the named tool with the given arguments and returns the result.
func ExecuteTool(name string, args map[string]string) string {
	switch name {
	/*
		case "shell":
			cmd, ok := args["command"]
			if !ok {
				return "error: missing 'command' argument"
			}
			return toolShell(cmd)
	*/
	case "http_get":
		url, ok := args["url"]
		if !ok {
			return "error: missing 'url' argument"
		}
		return toolHTTPGet(url)
	case "read_file":
		path, ok := args["path"]
		if !ok {
			return "error: missing 'path' argument"
		}
		return toolReadFile(path)
	default:
		return fmt.Sprintf("error: unknown tool '%s'. Available: shell, http_get, read_file", name)
	}
}

// ToolDefinitions returns the JSON schema descriptions for the LLM.
func ToolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		/*
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "shell",
					"description": "Execute a shell command and return stdout+stderr (30s timeout, 4KB cap)",
					"parameters": map[string]interface{}{
						"type":     "object",
						"required": []string{"command"},
						"properties": map[string]interface{}{
							"command": map[string]string{
								"type":        "string",
								"description": "The shell command to execute",
							},
						},
					},
				},
			},
		*/
		{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "http_get",
				"description": "Fetch a URL via HTTP GET and return the response (15s timeout, 4KB cap)",
				"parameters": map[string]interface{}{
					"type":     "object",
					"required": []string{"url"},
					"properties": map[string]interface{}{
						"url": map[string]string{
							"type":        "string",
							"description": "The URL to fetch",
						},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "read_file",
				"description": "Read the first 4KB of a file and return its contents",
				"parameters": map[string]interface{}{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]interface{}{
						"path": map[string]string{
							"type":        "string",
							"description": "Absolute path to the file to read",
						},
					},
				},
			},
		},
	}
}
