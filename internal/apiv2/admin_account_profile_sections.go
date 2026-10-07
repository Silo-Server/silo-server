package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The administrator's view of one profile's page layout: the same override
// set the profile_sections operations read and write for the acting profile,
// addressed instead by account and profile. Shapes match those operations so
// a client reuses its types and editor. Writes are audited.

// AdminProfileSectionService is the slice of *handlers.SectionHandler the
// admin profile-sections operations use. Each method answers 404 not_found
// when the profile does not belong to the account.
type AdminProfileSectionService interface {
	ListAccountProfileOverrides(ctx context.Context, q handlers.SectionOverridesQuery) ([]userstore.SectionOverride, error)
	SaveAccountProfileOverrides(ctx context.Context, q handlers.SectionOverridesQuery, writes []handlers.SectionOverrideWrite) error
	ResetAccountProfileOverrides(ctx context.Context, q handlers.SectionOverridesQuery) error
	ResolveAccountProfileSectionSettings(ctx context.Context, q handlers.SectionOverridesQuery, libraryID *int) ([]sections.ResolvedSection, error)
}

// AdminProfileSectionsInput addresses one page of one profile on an account.
type AdminProfileSectionsInput struct {
	ID        ID     `path:"id" doc:"The account" example:"7"`
	ProfileID string `path:"profile_id" minLength:"1" maxLength:"128" doc:"A profile of the account" example:"p-owner"`
	SectionOverridesScopeInput
}

// AdminProfileSectionsReplaceInput is the replaceAdminUserProfileSectionOverrides request.
type AdminProfileSectionsReplaceInput struct {
	ID        ID     `path:"id" doc:"The account" example:"7"`
	ProfileID string `path:"profile_id" minLength:"1" maxLength:"128" doc:"A profile of the account" example:"p-owner"`
	SectionOverridesScopeInput
	Body SectionOverrideSet
	// RawBody is the document as sent; see ProfileUpdateInput.
	RawBody []byte
}

const adminProfileSectionsPath = "/{id}/profiles/{profile_id}/sections"

func registerAdminProfileSections(reg *Registry) {
	Register(reg, adminProfileSectionsOperation(http.MethodGet, "", "listAdminUserProfileSectionOverrides",
		"List a profile's saved section overrides.",
		"One profile's saved overrides for one page, as listProfileSectionOverrides answers for the acting profile. 404 not_found when the profile does not belong to the account."),
		func(ctx context.Context, in *AdminProfileSectionsInput) (*SectionOverrideCollectionOutput, error) {
			svc, q, _, p := reg.adminProfileSectionsTarget(ctx, in.ID, in.ProfileID, in.SectionOverridesScopeInput)
			if p != nil {
				return nil, p
			}
			rows, err := svc.ListAccountProfileOverrides(ctx, q)
			if err != nil {
				return nil, serviceProblem(err)
			}
			return sectionOverrideCollectionOf(rows)
		})

	replace := adminProfileSectionsOperation(http.MethodPut, "", "replaceAdminUserProfileSectionOverrides",
		"Replace a profile's section overrides.",
		"Replaces one profile's override set for one page, with the body and validation of replaceProfileSectionOverrides. The recipe gate checks the account's own role, so an administrator cannot save a section the profile could not save itself. The write is audited. 404 not_found when the profile does not belong to the account.")
	replace.DefaultStatus = http.StatusNoContent
	Register(reg, replace, func(ctx context.Context, in *AdminProfileSectionsReplaceInput) (*struct{}, error) {
		svc, q, _, p := reg.adminProfileSectionsTarget(ctx, in.ID, in.ProfileID, in.SectionOverridesScopeInput)
		if p != nil {
			return nil, p
		}
		writes, p := sectionOverrideWritesOf(&SectionOverridesReplaceInput{SectionOverridesScopeInput: in.SectionOverridesScopeInput, Body: in.Body, RawBody: in.RawBody})
		if p != nil {
			return nil, p
		}
		if err := svc.SaveAccountProfileOverrides(ctx, q, writes); err != nil {
			return nil, sectionProblem(err)
		}
		return nil, nil
	})

	reset := adminProfileSectionsOperation(http.MethodDelete, "", "resetAdminUserProfileSectionOverrides",
		"Reset a profile's section overrides.",
		"Deletes one profile's override set for one page, so that profile follows the admin layout again. Other profiles keep theirs. The reset is audited. 404 not_found when the profile does not belong to the account.")
	reset.DefaultStatus = http.StatusNoContent
	Register(reg, reset, func(ctx context.Context, in *AdminProfileSectionsInput) (*struct{}, error) {
		svc, q, _, p := reg.adminProfileSectionsTarget(ctx, in.ID, in.ProfileID, in.SectionOverridesScopeInput)
		if p != nil {
			return nil, p
		}
		if err := svc.ResetAccountProfileOverrides(ctx, q); err != nil {
			return nil, serviceProblem(err)
		}
		return nil, nil
	})

	Register(reg, adminProfileSectionsOperation(http.MethodGet, "/settings", "getAdminUserProfileSectionSettings",
		"Get a page's sections as a profile's layout orders them.",
		"One page's sections with the profile's overrides applied, the shape getProfileSectionSettings answers. Unlike that read, no library-access filter applies: every section the layout orders is listed. 404 not_found when the profile does not belong to the account."),
		func(ctx context.Context, in *AdminProfileSectionsInput) (*ProfileSectionSettingCollectionOutput, error) {
			svc, q, libraryID, p := reg.adminProfileSectionsTarget(ctx, in.ID, in.ProfileID, in.SectionOverridesScopeInput)
			if p != nil {
				return nil, p
			}
			resolved, err := svc.ResolveAccountProfileSectionSettings(ctx, q, libraryID)
			if err != nil {
				return nil, serviceProblem(err)
			}
			return profileSectionSettingsOf(resolved)
		})
}

func adminProfileSectionsOperation(method, suffix, id, summary, description string) Operation {
	op := adminAccountOperation(method, adminProfileSectionsPath+suffix, id, false)
	op.Summary, op.Description = summary, description
	return op
}

// adminProfileSectionsTarget resolves the addressed account (404 when it does
// not exist) and page; the service confirms the profile belongs to it.
func (reg *Registry) adminProfileSectionsTarget(ctx context.Context, rawID ID, profileID string, in SectionOverridesScopeInput) (AdminProfileSectionService, handlers.SectionOverridesQuery, *int, *Problem) {
	accounts, p := reg.adminAccounts()
	if p != nil {
		return nil, handlers.SectionOverridesQuery{}, nil, p
	}
	svc := reg.deps.AdminProfileSections
	if svc == nil {
		return nil, handlers.SectionOverridesQuery{}, nil, unavailable("section")
	}
	id, p := adminAccountID(rawID)
	if p != nil {
		return nil, handlers.SectionOverridesQuery{}, nil, p
	}
	if _, err := accounts.GetAdminAccount(ctx, id); err != nil {
		return nil, handlers.SectionOverridesQuery{}, nil, adminAccountError(err)
	}
	q, libraryID, p := pageOverridesQuery(id, profileID, in)
	if p != nil {
		return nil, handlers.SectionOverridesQuery{}, nil, p
	}
	return svc, q, libraryID, nil
}
