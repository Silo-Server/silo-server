package apiv2

import (
	"context"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// Requester options: the download server settings a requester or approving
// admin may choose for one request. Kept apart from requests.go to stay under
// the file size limit.

// RequestOptionChoice is one setting the requester chose for a request.
type RequestOptionChoice struct {
	Key        string `json:"key" doc:"The option's key" example:"radarr_profile_id"`
	Label      string `json:"label" doc:"The option's name" example:"Quality"`
	Value      string `json:"value" doc:"The chosen value" example:"4"`
	ValueLabel string `json:"value_label" doc:"The chosen value's name when it was chosen" example:"Ultra-HD"`
}

func requestOptionChoicesOf(choices mediarequests.RequesterChoices) []RequestOptionChoice {
	out := make([]RequestOptionChoice, 0, len(choices.Choices))
	for _, c := range choices.Choices {
		out = append(out, RequestOptionChoice{Key: c.Key, Label: c.Label, Value: c.Value, ValueLabel: c.ValueLabel})
	}
	return out
}

// RequestOption is one download server setting the viewer may choose when
// requesting a title.
type RequestOption struct {
	Key     string               `json:"key" doc:"The key to send in createRequest's options" example:"radarr_profile_id"`
	Label   string               `json:"label" doc:"The option's name" example:"Quality"`
	Default string               `json:"default" doc:"The value in effect when the requester chooses nothing, for preselecting; always one of choices" example:"default"`
	Choices []RequestOptionValue `json:"choices" doc:"The values on offer; never empty"`
}

// RequestOptionValue is one value of a RequestOption.
type RequestOptionValue struct {
	Value string `json:"value" example:"4"`
	Label string `json:"label" example:"Ultra-HD"`
}

// RequestOptionList is the listRequestOptions response body.
type RequestOptionList struct {
	Options []RequestOption `json:"options" doc:"Empty, never null, when the title's download server offers no choice, or its choices cannot be read right now"`
}

// RequestOptionListOutput is the listRequestOptions response.
type RequestOptionListOutput struct {
	Body RequestOptionList
}

// listRequestOptions answers which download server settings the viewer may
// choose for a title. It asks the server's request plugin, so clients call it
// when the requester opens the request control, not for every title shown.
func (reg *Registry) listRequestOptions(ctx context.Context, in *RequestMediaDetailInput) (*RequestOptionListOutput, error) {
	svc, viewer, p := reg.requestViewer(ctx)
	if p != nil {
		return nil, p
	}
	options, err := svc.RequesterOptions(ctx, viewer, mediarequests.MediaType(in.MediaType), in.TMDBID)
	if err != nil {
		return nil, requestProblem(err)
	}
	return &RequestOptionListOutput{Body: requestOptionListOf(options)}, nil
}

func requestOptionListOf(options []mediarequests.RequesterOption) RequestOptionList {
	out := RequestOptionList{Options: make([]RequestOption, 0, len(options))}
	for _, option := range options {
		values := make([]RequestOptionValue, 0, len(option.Choices))
		for _, choice := range option.Choices {
			values = append(values, RequestOptionValue{Value: choice.Value, Label: choice.Label})
		}
		out.Options = append(out.Options, RequestOption{Key: option.Key, Label: option.Label, Default: option.Default, Choices: values})
	}
	return out
}
