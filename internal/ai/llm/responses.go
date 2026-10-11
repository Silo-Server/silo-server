package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type responseReasoning struct {
	Effort string `json:"effort"`
}

type responseTextFormat struct {
	Format chatResponseFormat `json:"format"`
}

type responseRequest struct {
	Model     string              `json:"model"`
	Input     []Message           `json:"input"`
	Store     bool                `json:"store"`
	Stream    bool                `json:"stream"`
	Reasoning *responseReasoning  `json:"reasoning,omitempty"`
	Text      *responseTextFormat `json:"text,omitempty"`
}

func (c *Client) chatGPT(ctx context.Context, cfg Config, messages []Message, jsonObject bool) (string, error) {
	if cfg.ChatGPTToken == nil {
		return "", errors.New("connect a ChatGPT account in AI Services")
	}
	input := make([]Message, len(messages))
	for i, m := range messages {
		if m.Role == "system" {
			m.Role = "developer"
		}
		input[i] = m
	}
	req := responseRequest{Model: cfg.ChatModel, Input: input, Store: false, Stream: true}
	if cfg.ReasoningEffort != "" {
		req.Reasoning = &responseReasoning{Effort: cfg.ReasoningEffort}
	}
	if jsonObject {
		req.Text = &responseTextFormat{Format: chatResponseFormat{Type: chatJSONFormat}}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	var content string
	httpClient := *c.chatHTTP
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var requestToken string
	for attempt := range 2 {
		err = c.doReaderWithRetry(ctx, &httpClient, "ChatGPT plan",
			func() (*http.Request, error) {
				token, err := cfg.ChatGPTToken(ctx)
				if err != nil {
					return nil, err
				}
				requestToken = token
				// Subscription credentials only go to the documented OpenAI endpoint,
				// never to an administrator's custom API-compatible base URL.
				r, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
				if err != nil {
					return nil, err
				}
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Accept", "text/event-stream")
				r.Header.Set("Authorization", "Bearer "+token)
				return r, nil
			}, func(data io.Reader) error {
				var parseErr error
				content, parseErr = readResponseStream(data)
				return parseErr
			})
		unauthorized, _ := errors.AsType[*HTTPError](err)
		if unauthorized == nil || unauthorized.StatusCode != http.StatusUnauthorized || cfg.ChatGPTTokenRejected == nil {
			break
		}
		if rejectErr := cfg.ChatGPTTokenRejected(ctx, requestToken, attempt == 1); rejectErr != nil {
			return "", rejectErr
		}
		if attempt == 1 {
			break
		}
	}
	return content, err
}

// readResponseStream requires the terminal completion event. Partial text,
// including text preceding a late usage-limit error, is never published.
func readResponseStream(data io.Reader) (string, error) {
	var text strings.Builder
	completed := false
	scanner := bufio.NewScanner(io.LimitReader(data, 32<<20))
	scanner.Buffer(make([]byte, 4096), 8<<20)
	var eventData []string
	consume := func() error {
		if len(eventData) == 0 {
			return nil
		}
		raw := strings.Join(eventData, "\n")
		eventData = nil
		if raw == "[DONE]" {
			return nil
		}
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"response"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return fmt.Errorf("invalid ChatGPT response event: %w", err)
		}
		switch event.Type {
		case "response.output_text.delta":
			text.WriteString(event.Delta)
		case "response.completed":
			completed = true
		case "response.failed", "error":
			code := event.Code
			if event.Response.Error != nil {
				code = event.Response.Error.Code
			}
			if code == "subscription_sharing_usage_unavailable" {
				return errors.New("ChatGPT usage is temporarily unavailable")
			}
			if code == "subscription_sharing_usage_limit_exceeded" {
				return &permanentError{ErrQuotaExhausted}
			}
			return &permanentError{errors.New("ChatGPT request failed; check the account and ChatGPT usage settings")}
		case "response.incomplete":
			return &permanentError{errors.New("ChatGPT returned an incomplete response")}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return "", err
			}
			if completed {
				break
			}
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			eventData = append(eventData, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if err := consume(); err != nil {
		return "", err
	}
	if !completed || text.Len() == 0 {
		return "", errors.New("ChatGPT stream ended before a complete text response")
	}
	return text.String(), nil
}
