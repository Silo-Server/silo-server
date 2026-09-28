package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type DownloadSubscriptionMutationService interface {
	CreateSubscriptionMonitor(context.Context, int, downloads.SubscriptionRequest, catalogpkg.AccessFilter) (*downloads.Subscription, error)
	UpdateSubscriptionMonitor(context.Context, int, string, string, string, downloads.SubscriptionPatch, catalogpkg.AccessFilter, func(*downloads.Subscription) error) (*downloads.Subscription, error)
	DeleteSubscriptionMonitor(context.Context, int, string, string, string, func(*downloads.Subscription) error) error
}
type DownloadSubscriptionCreateBody struct {
	SeriesID        string `json:"series_id" minLength:"1" maxLength:"128"`
	Mode            string `json:"mode" enum:"all,future,latest_season,specific_seasons"`
	SeasonNumbers   []int  `json:"season_numbers,omitempty" maxItems:"10000"`
	DeleteWatched   bool   `json:"delete_watched"`
	MaxStorageBytes int64  `json:"max_storage_bytes" minimum:"0"`
}
type DownloadSubscriptionCreateInput struct {
	DeviceID       string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
	DeviceName     string `header:"X-Silo-Device-Name" maxLength:"256"`
	DevicePlatform string `header:"X-Silo-Device-Platform" maxLength:"128"`
	Body           DownloadSubscriptionCreateBody
}
type DownloadSubscriptionPatchBody struct {
	Mode            Patch[string] `json:"mode,omitzero"`
	SeasonNumbers   Patch[[]int]  `json:"season_numbers,omitzero"`
	DeleteWatched   Patch[bool]   `json:"delete_watched,omitzero"`
	MaxStorageBytes Patch[int64]  `json:"max_storage_bytes,omitzero"`
	Active          Patch[bool]   `json:"active,omitzero"`
}
type DownloadSubscriptionPatchInput struct {
	ID          string `path:"id" minLength:"1"`
	DeviceID    string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        DownloadSubscriptionPatchBody
}
type DownloadSubscriptionDeleteInput struct {
	ID          string `path:"id" minLength:"1"`
	DeviceID    string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

func registerDownloadSubscriptionMutations(reg *Registry) {
	create := Operation{Operation: humaOp(http.MethodPost, Prefix+"/downloads/subscriptions", "createDownloadSubscription", "downloads", "Create a monitor or return the device's existing monitor for the series."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}
	create.Description = "An existing monitor keeps its options and forgets the episodes deleted under it, so the next sync can register them again. Sync explicitly after receipt; do not automatically resend an uncertain create."
	create.MaxBodyBytes = 128 << 10
	Register(reg, create, reg.createDownloadSubscription)
	update := Operation{Operation: humaOp(http.MethodPatch, Prefix+"/downloads/subscriptions/{id}", "updateDownloadSubscription", "downloads", "Edit monitor options under the current validator. Sync explicitly after changing scope; null fields are rejected."), Class: ClassProfileScoped, ServiceBacked: true, Guarded: true, RetrySafety: RetrySafetyNaturalIdempotent}
	update.MaxBodyBytes = 128 << 10
	Register(reg, update, reg.updateDownloadSubscription)
	remove := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/downloads/subscriptions/{id}", "deleteDownloadSubscription", "downloads", "Stop monitoring under the current validator, retaining already-registered downloads."), Class: ClassProfileScoped, ServiceBacked: true, Guarded: true, DemoRestricted: true, RetrySafety: RetrySafetyNaturalIdempotent}
	remove.DefaultStatus = http.StatusNoContent
	Register(reg, remove, reg.deleteDownloadSubscription)
}
func subscriptionMutationProblem(err error) *Problem {
	if p, ok := errors.AsType[*Problem](err); ok {
		return p
	}
	switch {
	case errors.Is(err, downloads.ErrInvalidSubscriptionMode), errors.Is(err, downloads.ErrSeasonsRequired), errors.Is(err, downloads.ErrInvalidSeasonNumbers), errors.Is(err, downloads.ErrNotSeries):
		return NewProblem(TypeMalformedRequest, err.Error())
	default:
		return downloadProblem(err)
	}
}
func subscriptionOutput(row *downloads.Subscription) *DownloadSubscriptionOutput {
	out := downloadSubscriptionOf(row)
	return &DownloadSubscriptionOutput{ETag: out.ETag, CacheControl: "private, no-cache", Body: out}
}
func (reg *Registry) createDownloadSubscription(ctx context.Context, in *DownloadSubscriptionCreateInput) (*DownloadSubscriptionOutput, error) {
	if reg.deps.DownloadSubscriptionMutations == nil {
		return nil, unavailable("download subscription mutations")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	row, err := reg.deps.DownloadSubscriptionMutations.CreateSubscriptionMonitor(ctx, user, downloads.SubscriptionRequest{SeriesID: in.Body.SeriesID, Mode: in.Body.Mode, SeasonNumbers: in.Body.SeasonNumbers, DeleteWatched: in.Body.DeleteWatched, MaxStorageBytes: in.Body.MaxStorageBytes, ProfileID: profile, DeviceID: in.DeviceID, DeviceName: in.DeviceName, DevicePlatform: in.DevicePlatform}, handlers.AccessFilterFromContext(ctx, ""))
	if err != nil {
		return nil, subscriptionMutationProblem(err)
	}
	return subscriptionOutput(row), nil
}
func subscriptionPatchPointer[T any](value Patch[T]) *T {
	if !value.Present {
		return nil
	}
	return new(value.Value)
}
func (reg *Registry) updateDownloadSubscription(ctx context.Context, in *DownloadSubscriptionPatchInput) (*DownloadSubscriptionOutput, error) {
	if reg.deps.DownloadSubscriptionMutations == nil {
		return nil, unavailable("download subscription mutations")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	b := in.Body
	if b.Mode.Null || b.SeasonNumbers.Null || b.DeleteWatched.Null || b.MaxStorageBytes.Null || b.Active.Null {
		return nil, NewProblem(TypeMalformedRequest, "Monitor fields cannot be null.")
	}
	if b.MaxStorageBytes.Present && b.MaxStorageBytes.Value < 0 || len(b.SeasonNumbers.Value) > 10000 {
		return nil, NewProblem(TypeMalformedRequest, "Invalid monitor storage or season bounds.")
	}
	patch := downloads.SubscriptionPatch{Mode: subscriptionPatchPointer(b.Mode), SeasonNumbers: subscriptionPatchPointer(b.SeasonNumbers), DeleteWatched: subscriptionPatchPointer(b.DeleteWatched), MaxStorageBytes: subscriptionPatchPointer(b.MaxStorageBytes), Active: subscriptionPatchPointer(b.Active)}
	row, err := reg.deps.DownloadSubscriptionMutations.UpdateSubscriptionMonitor(ctx, user, profile, in.DeviceID, in.ID, patch, handlers.AccessFilterFromContext(ctx, ""), subscriptionGuard(in.IfMatch, in.IfNoneMatch))
	if err != nil {
		return nil, subscriptionMutationProblem(err)
	}
	return subscriptionOutput(row), nil
}
func subscriptionGuard(match, none string) func(*downloads.Subscription) error {
	return func(row *downloads.Subscription) error {
		tag, _ := ParseEntityTag(downloadSubscriptionOf(row).ETag)
		if p := EvaluateGuardedPreconditions(match, none, tag); p != nil {
			return p
		}
		return nil
	}
}
func (reg *Registry) deleteDownloadSubscription(ctx context.Context, in *DownloadSubscriptionDeleteInput) (*struct{}, error) {
	if reg.deps.DownloadSubscriptionMutations == nil {
		return nil, unavailable("download subscription mutations")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	err := reg.deps.DownloadSubscriptionMutations.DeleteSubscriptionMonitor(ctx, user, profile, in.DeviceID, in.ID, subscriptionGuard(in.IfMatch, in.IfNoneMatch))
	if err != nil {
		return nil, subscriptionMutationProblem(err)
	}
	return &struct{}{}, nil
}
