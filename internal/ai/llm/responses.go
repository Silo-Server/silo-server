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

const subscriptionUsageLimitExceeded = "subscription_sharing_usage_limit_exceeded"
const subscriptionUserNotEligible = "subscription_sharing_user_not_eligible"
const subscriptionInvalidUser = "subscription_sharing_invalid_user"
const responseFailedEvent = "response.failed"

var ErrPlanIneligible = errors.New("this account cannot share ChatGPT plan usage; check ChatGPT usage settings")

// SSE accepts CR, LF, and CRLF. Remember CR across reads so a following LF
// belongs to the same ending, without delaying a completed frame for lookahead.
func splitEventLines() bufio.SplitFunc {
	afterCR := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		start := 0
		if afterCR && len(data) > 0 {
			afterCR = false
			if data[0] == '\n' {
				start = 1
			}
		}
		for i := start; i < len(data); i++ {
			switch data[i] {
			case '\r':
				afterCR = true
				return i + 1, data[start:i], nil
			case '\n':
				return i + 1, data[start:i], nil
			}
		}
		if atEOF && len(data) > start {
			return len(data), data[start:], nil
		}
		return start, nil, nil
	}
}

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
	if cfg.ChatGPTTokenSource == nil {
		return "", errors.New("connect a ChatGPT account in AI Services")
	}
	tokenSource, err := cfg.ChatGPTTokenSource(ctx)
	if err != nil {
		return "", err
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
				token, err := tokenSource(ctx)
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
		if errors.Is(err, ErrQuotaExhausted) && cfg.ChatGPTUsageLimited != nil {
			if pauseErr := cfg.ChatGPTUsageLimited(ctx, requestToken); pauseErr != nil {
				return "", pauseErr
			}
		}
		unauthorized, _ := errors.AsType[*HTTPError](err)
		if errors.Is(err, ErrPlanIneligible) || (unauthorized != nil && unauthorized.StatusCode == http.StatusForbidden && unauthorized.Code == subscriptionUserNotEligible) {
			if cfg.ChatGPTPlanRejected != nil {
				if rejectErr := cfg.ChatGPTPlanRejected(ctx, requestToken); rejectErr != nil {
					return "", rejectErr
				}
			}
			return "", ErrPlanIneligible
		}
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
	if err == nil && cfg.ChatGPTUsageVerified != nil {
		if verifyErr := cfg.ChatGPTUsageVerified(ctx, requestToken); verifyErr != nil {
			return "", verifyErr
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
	scanner.Split(splitEventLines())
	var eventData []string
	firstLine := true
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
		case responseFailedEvent, "error":
			code := event.Code
			if event.Response.Error != nil {
				code = event.Response.Error.Code
			}
			if code == "subscription_sharing_usage_unavailable" {
				return errors.New("ChatGPT usage is temporarily unavailable")
			}
			if code == "subscription_sharing_user_unavailable" {
				return errors.New("ChatGPT subscriber information is temporarily unavailable")
			}
			if code == "server_error" {
				return errors.New("ChatGPT is temporarily unavailable")
			}
			if code == subscriptionUsageLimitExceeded {
				return &permanentError{ErrQuotaExhausted}
			}
			if code == subscriptionUserNotEligible {
				return &permanentError{ErrPlanIneligible}
			}
			if code == subscriptionInvalidUser {
				// A streamed subscriber rejection uses the same refresh-once path
				// as an HTTP 401, without retrying the rejected token first.
				return &permanentError{&HTTPError{StatusCode: http.StatusUnauthorized, Code: code, message: "ChatGPT subscriber credentials were rejected"}}
			}
			return &permanentError{errors.New("ChatGPT request failed; check the account and ChatGPT usage settings")}
		case "response.incomplete":
			return &permanentError{errors.New("ChatGPT returned an incomplete response")}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
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
