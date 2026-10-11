// Package reasoning defines the effort vocabulary shared by AI settings and requests.
package reasoning

import (
	"fmt"
	"slices"
	"strings"
)

const (
	levelNone      = "none"
	levelMinimal   = "minimal"
	levelLow       = "low"
	levelMedium    = "medium"
	levelHigh      = "high"
	levelExtraHigh = "xhigh"
	levelMax       = "max"
)

var Levels = []string{levelNone, levelMinimal, levelLow, levelMedium, levelHigh, levelExtraHigh, levelMax}

// Validate accepts an empty effort as the provider's default. Unknown models
// are checked by the provider; the known OpenAI families have narrower limits.
func Validate(model, effort string) error {
	if effort == "" {
		return nil
	}
	if !slices.Contains(Levels, effort) {
		return fmt.Errorf("unsupported reasoning effort %q", effort)
	}
	levels := ForModel(model)
	if !slices.Contains(levels, effort) {
		return fmt.Errorf("model %q does not support reasoning effort %q", model, effort)
	}
	return nil
}

// ForModel returns known supported levels, or the configurable vocabulary for
// a custom model. Gateway prefixes such as openai/ are accepted.
func ForModel(model string) []string {
	model = strings.TrimPrefix(strings.ToLower(model), "openai/")
	switch {
	case strings.HasPrefix(model, "gpt-4"), strings.HasPrefix(model, "gpt-3"):
		return []string{}
	case strings.HasPrefix(model, "gpt-6.1-sol"):
		return []string{levelLow, levelMedium, levelHigh, levelExtraHigh, levelMax}
	case strings.HasPrefix(model, "gpt-6-astra"):
		return []string{levelLow, levelMedium, levelHigh, levelExtraHigh, levelMax}
	case strings.HasPrefix(model, "gpt-6-sol"), strings.HasPrefix(model, "gpt-6-luna"), strings.HasPrefix(model, "gpt-5.6"):
		return []string{levelNone, levelLow, levelMedium, levelHigh, levelExtraHigh, levelMax}
	case strings.HasPrefix(model, "gpt-5-pro"):
		return []string{levelHigh}
	case strings.HasPrefix(model, "gpt-5.2-pro"), strings.HasPrefix(model, "gpt-5.4-pro"), strings.HasPrefix(model, "gpt-5.5-pro"):
		return []string{levelMedium, levelHigh, levelExtraHigh}
	case strings.HasPrefix(model, "gpt-5.2"), strings.HasPrefix(model, "gpt-5.4"), strings.HasPrefix(model, "gpt-5.5"):
		return []string{levelNone, levelLow, levelMedium, levelHigh, levelExtraHigh}
	case strings.HasPrefix(model, "gpt-5.1"):
		return []string{levelNone, levelLow, levelMedium, levelHigh}
	case model == "gpt-5", strings.HasPrefix(model, "gpt-5-mini"), strings.HasPrefix(model, "gpt-5-nano"), strings.HasPrefix(model, "gpt-5-20"):
		return []string{levelMinimal, levelLow, levelMedium, levelHigh}
	case strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
		return []string{levelLow, levelMedium, levelHigh}
	default:
		return slices.Clone(Levels)
	}
}

// OmitTemperature identifies reasoning requests that reject sampling settings.
func OmitTemperature(model, effort string) bool {
	model = strings.TrimPrefix(strings.ToLower(model), "openai/")
	return effort != "" || strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6") ||
		strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4")
}
