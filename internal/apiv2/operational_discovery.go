package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

// CompatConnectInfoService exposes only the settings a signed-in account needs
// to connect a compatibility client; implementation details remain admin-only.
type CompatConnectInfoService interface {
	GetConnectInfo(context.Context) handlers.CompatConnectInfoResponse
}

type CompatConnectInfoOutput struct {
	Body CompatConnectInfoResponse
}

type ImageCapabilities struct {
	Capability
	SeasonListArtworkParam string                     `json:"season_list_artwork_param" doc:"Season-list boolean query parameter; false omits poster URLs and thumbhashes"`
	Param                  string                     `json:"param"`
	Sizes                  []imagesize.Size           `json:"sizes"`
	Widths                 map[string]ImageSizeWidths `json:"widths"`
	OriginalMaxWidthPx     int                        `json:"original_max_width_px"`
	StorageBackend         string                     `json:"storage_backend" enum:"local,s3"`
	Delivery               string                     `json:"delivery" enum:"server,direct"`
}

type ImageCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ImageCapabilities
}

func registerOperationalDiscovery(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/compat/connect-info", "getCompatConnectInfo", "compat", "Connection information for compatibility clients."), Class: ClassAuthenticated, ServiceBacked: true},
		func(ctx context.Context, _ *struct{}) (*CompatConnectInfoOutput, error) {
			if reg.deps.CompatConnectInfo == nil {
				return nil, NewProblem(TypeDependencyUnavailable, "Compatibility connection information is not configured.")
			}
			v := reg.deps.CompatConnectInfo.GetConnectInfo(ctx)
			return &CompatConnectInfoOutput{Body: CompatConnectInfoResponse{Jellyfin: v.Jellyfin, Account: CompatConnectAccountInfo(v.Account)}}, nil
		})
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/images/capabilities", "getImageCapabilities", "images", "Available image sizes and their pixel widths."), Class: ClassProfileScoped, ProfileOptional: true},
		func(context.Context, *CapabilityInput) (*ImageCapabilitiesOutput, error) {
			view := handlers.GetImagesCapability(reg.deps.ArtworkBackend)
			widths := make(map[string]ImageSizeWidths, len(view.Widths))
			for key, value := range view.Widths {
				widths[key] = ImageSizeWidths(value)
			}
			return &ImageCapabilitiesOutput{Body: ImageCapabilities{
				SeasonListArtworkParam: view.SeasonListArtworkParam,
				StorageBackend:         view.StorageBackend, Delivery: view.Delivery,
				Capability: Capability{State: StateAvailable}, Param: view.Param, Sizes: view.Sizes, Widths: widths, OriginalMaxWidthPx: view.OriginalMaxWidthPx,
			}}, nil
		})
}

// CompatConnectAccountInfo is the native transport projection, independent of handler views.
type CompatConnectAccountInfo struct {
	// PasswordLoginAvailable reports whether this account can authenticate
	// with a password at all. Compat login is hardwired to the local provider,
	// so SSO/plugin-provisioned accounts cannot sign in to a Jellyfin client
	// no matter what they type.
	PasswordLoginAvailable bool `json:"password_login_available"`
}

// CompatConnectInfoResponse is the native transport projection, independent of handler views.
type CompatConnectInfoResponse struct {
	Jellyfin jellycompat.ConnectInfo  `json:"jellyfin"`
	Account  CompatConnectAccountInfo `json:"account"`
}

// ImageSizeWidths is the native transport projection, independent of handler views.
type ImageSizeWidths struct {
	Small  int `json:"small"`
	Medium int `json:"medium"`
	Large  int `json:"large"`
}
