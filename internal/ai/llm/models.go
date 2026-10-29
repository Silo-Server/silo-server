package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// ModelListing selects the API a provider lists its models through.
type ModelListing string

const (
	// ModelListingOpenAI is GET /v1/models on an OpenAI-compatible endpoint.
	ModelListingOpenAI ModelListing = "openai"
	// ModelListingGemini is Gemini's native models.list.
	ModelListingGemini ModelListing = "gemini"
	// ModelListingOllama is Ollama's /api/tags.
	ModelListingOllama ModelListing = "ollama"
)

// Model kinds a listing reports. An empty kind is a model none of Silo's jobs
// can use, such as image generation or text-to-speech.
const (
	ModelKindChat       = "chat"
	ModelKindEmbeddings = "embeddings"
	ModelKindSpeech     = "speech"
)

// ListedModel is one model a provider offers.
type ListedModel struct {
	ID   string
	Kind string
}

// ErrModelListingUnsupported means the endpoint answered but has no model
// list; the administrator types model names instead.
var ErrModelListingUnsupported = errors.New("the provider does not list its models")

const maxModelListBytes = 16 << 20

// modelListHTTP never follows a redirect: the request carries the provider's
// key, which a redirect would hand to another host.
var modelListHTTP = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// ListModels returns the models a provider offers, sorted by id. Kinds come
// from the provider when it reports them and from the model's name otherwise.
func ListModels(ctx context.Context, listing ModelListing, baseURL, apiKey string) ([]ListedModel, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	var url string
	switch listing {
	case ModelListingGemini:
		url = baseURL + "/v1beta/models?pageSize=1000"
	case ModelListingOllama:
		url = baseURL + "/api/tags"
	default:
		url = endpointURL(baseURL, "models")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		if listing == ModelListingGemini {
			req.Header.Set("x-goog-api-key", apiKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	resp, err := modelListHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return nil, ErrModelListingUnsupported
	case resp.StatusCode >= 300:
		return nil, &HTTPError{StatusCode: resp.StatusCode, message: fmt.Sprintf("model list returned %d", resp.StatusCode)}
	}

	var models []ListedModel
	switch listing {
	case ModelListingGemini:
		var decoded struct {
			Models []struct {
				Name    string   `json:"name"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, ErrModelListingUnsupported
		}
		for _, m := range decoded.Models {
			kind := ""
			switch {
			case slices.Contains(m.Methods, "embedContent"):
				kind = ModelKindEmbeddings
			case slices.Contains(m.Methods, "generateContent"):
				kind = ModelKindChat
			}
			models = append(models, ListedModel{ID: strings.TrimPrefix(m.Name, "models/"), Kind: kind})
		}
	case ModelListingOllama:
		var decoded struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, ErrModelListingUnsupported
		}
		for _, m := range decoded.Models {
			models = append(models, ListedModel{ID: m.Name, Kind: modelKindFromName(m.Name)})
		}
	default:
		var decoded struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, ErrModelListingUnsupported
		}
		for _, m := range decoded.Data {
			models = append(models, ListedModel{ID: m.ID, Kind: modelKindFromName(m.ID)})
		}
	}
	models = slices.DeleteFunc(models, func(m ListedModel) bool { return strings.TrimSpace(m.ID) == "" })
	slices.SortFunc(models, func(a, b ListedModel) int { return strings.Compare(a.ID, b.ID) })
	return slices.CompactFunc(models, func(a, b ListedModel) bool { return a.ID == b.ID }), nil
}

// modelKindFromName guesses a model's kind from its id, for providers whose
// list does not say.
func modelKindFromName(id string) string {
	name := strings.ToLower(id)
	contains := func(parts ...string) bool {
		return slices.ContainsFunc(parts, func(part string) bool { return strings.Contains(name, part) })
	}
	switch {
	case contains("embed", "minilm", "bge-", "e5-", "nomic"):
		return ModelKindEmbeddings
	case contains("whisper", "transcribe"):
		return ModelKindSpeech
	case contains("tts", "dall-e", "image", "moderation", "realtime", "sora", "audio", "rerank", "guard"):
		return ""
	default:
		return ModelKindChat
	}
}
