package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
)

const adminChatGPTSignInRequiredMessage = "Sign in with ChatGPT in AI Services settings."

const adminInvalidSettingsCode = "invalid_settings"
const adminAIChatModelKey = "ai.chat_model"
const adminAIReasoningEffortKey = "ai.reasoning_effort"
const adminAIAuthModeKey = "ai.auth_mode"
const adminAILegacyChatModelKey = "subtitle_ai.chat_model"

func (h *AdminHandler) prepareAIModelValidation(ctx context.Context, changes map[string]string) (*chatgpt.ModelValidation, error) {
	if h.ChatGPT == nil {
		return nil, nil
	}
	touched := false
	for _, key := range []string{adminAIAuthModeKey, adminAIChatModelKey, adminAILegacyChatModelKey, adminAIReasoningEffortKey} {
		if _, ok := changes[key]; ok {
			touched = true
			break
		}
	}
	if !touched {
		return nil, nil
	}
	validation, err := h.ChatGPT.PrepareModelValidation(ctx, changes)
	if err != nil {
		message := "Could not verify ChatGPT model availability. Refresh the connection and try again."
		if errors.Is(err, chatgpt.ErrSignInRequired) {
			message = adminChatGPTSignInRequiredMessage
		}
		return nil, &APIError{Status: http.StatusBadRequest, Code: adminInvalidSettingsCode, Message: message}
	}
	return validation, nil
}
