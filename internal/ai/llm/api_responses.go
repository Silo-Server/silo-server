package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/ai/reasoning"
)

// Ultrafast is supported by Responses, rather than Chat Completions. Use the
// same API-key provider and retry policy, without subscription credentials.
func (c *Client) apiResponses(ctx context.Context, cfg Config, messages []Message, jsonObject bool) (string, error) {
	req := responseRequest{Model: cfg.ChatModel, Input: messages, ServiceTier: cfg.ServiceTier}
	if cfg.ReasoningEffort != "" {
		req.Reasoning = &responseReasoning{Effort: cfg.ReasoningEffort}
	}
	if !reasoning.OmitTemperature(cfg.ChatModel, cfg.ReasoningEffort) {
		req.Temperature = new(float32(0.2))
	}
	if jsonObject {
		req.Text = &responseTextFormat{Format: chatResponseFormat{Type: chatJSONFormat}}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal responses request: %w", err)
	}
	var content string
	err = c.doWithRetry(ctx, c.chatHTTP, "Responses API", func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL(cfg.BaseURL, "responses"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		if cfg.APIKey != "" {
			r.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
		return r, nil
	}, func(data []byte) error {
		var response struct {
			Status string `json:"status"`
			Error  *struct {
				Code string `json:"code"`
			} `json:"error"`
			Output []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return fmt.Errorf("decode responses result: %w", err)
		}
		if response.Error != nil {
			if isExhaustedQuota(data) {
				return &permanentError{ErrQuotaExhausted}
			}
			if response.Error.Code == responseServerError || response.Error.Code == "rate_limit_exceeded" {
				return errors.New("responses API is temporarily unavailable")
			}
		}
		if response.Status != "completed" || response.Error != nil {
			return &permanentError{errors.New("responses API did not complete the text response")}
		}
		var text strings.Builder
		for _, item := range response.Output {
			if item.Type != "message" {
				continue
			}
			for _, part := range item.Content {
				if part.Type == "refusal" {
					return &permanentError{errors.New("responses API refused the text request")}
				}
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		}
		if text.Len() == 0 {
			return &permanentError{errors.New("responses API returned no text")}
		}
		content = text.String()
		return nil
	})
	return content, err
}
