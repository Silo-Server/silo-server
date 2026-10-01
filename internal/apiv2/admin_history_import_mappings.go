package apiv2

import (
	"context"
	"errors"

	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

type AdminHistoryImportMappingCreateInput struct {
	Body struct {
		SourceID         ID     `json:"source_id" pattern:"^[1-9][0-9]*$"`
		ExternalUserID   string `json:"external_user_id" minLength:"1"`
		ExternalUserName string `json:"external_user_name"`
		SiloUserID       ID     `json:"silo_user_id" pattern:"^[1-9][0-9]*$"`
		SiloProfileID    ID     `json:"silo_profile_id" minLength:"1"`
	}
}
type AdminHistoryImportMappingUpdateInput struct {
	RawBody []byte
	AdminHistoryImportIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		SiloUserID    *ID `json:"silo_user_id,omitempty" nullable:"false" pattern:"^[1-9][0-9]*$"`
		SiloProfileID *ID `json:"silo_profile_id,omitempty" nullable:"false" minLength:"1"`
	}
}

func (reg *Registry) adminHistoryMappingVersion(ctx context.Context, id int, match, none string) (int64, *Problem) {
	s, p := reg.adminHistoryImports()
	if p != nil {
		return 0, p
	}
	row, err := s.GetMapping(ctx, id)
	if err != nil {
		return 0, adminHistoryProblem(err)
	}
	return adminHistoryGuard(match, none, adminHistoryTag(ctx, "mapping", id, row.Revision), row.Revision)
}
func (reg *Registry) adminHistoryMappingWriteResult(ctx context.Context, id int, row *historyimport.UserMapping, err error) (*AdminHistoryImportMappingOutput, error) {
	if errors.Is(err, historyimport.ErrStaleRevision) {
		current, e := reg.getAdminHistoryMapping(ctx, &AdminHistoryImportIDInput{ID: ID(strconv.Itoa(id))})
		if e != nil {
			return nil, e
		}
		return nil, adminHistoryProblem(err).WithHeader("ETag", current.ETag)
	}
	if err != nil {
		return nil, adminHistoryProblem(err)
	}
	return &AdminHistoryImportMappingOutput{ETag: adminHistoryTag(ctx, "mapping", id, row.Revision).String(), Body: adminHistoryMappingOf(row)}, nil
}
func registerAdminHistoryImportMappings(reg *Registry, op func(string, string, string, bool) Operation) {
	create := op(http.MethodPost, "/admin/history-imports/mappings", "createAdminHistoryImportMapping", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *AdminHistoryImportMappingCreateInput) (*AdminHistoryImportMappingOutput, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		source, p := adminHistoryID(in.Body.SourceID)
		if p != nil {
			return nil, p
		}
		user, p := adminHistoryID(in.Body.SiloUserID)
		if p != nil {
			return nil, p
		}
		row, err := s.CreateMapping(ctx, historyimport.CreateMappingInput{SourceID: source, ExternalUserID: in.Body.ExternalUserID, ExternalUserName: in.Body.ExternalUserName, SiloUserID: user, SiloProfileID: string(in.Body.SiloProfileID)})
		if err != nil {
			return nil, adminHistoryProblem(err)
		}
		out, err := reg.adminHistoryMappingWriteResult(ctx, row.ID, row, nil)
		if err == nil {
			out.Location = Prefix + "/admin/history-imports/mappings/" + strconv.Itoa(row.ID)
		}
		return out, err
	})
	Register(reg, op(http.MethodPut, "/admin/history-imports/mappings/{id}", "updateAdminHistoryImportMapping", true), func(ctx context.Context, in *AdminHistoryImportMappingUpdateInput) (*AdminHistoryImportMappingOutput, error) {
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		expected, p := reg.adminHistoryMappingVersion(ctx, id, in.IfMatch, in.IfNoneMatch)
		if p != nil {
			return nil, p
		}
		input := historyimport.UpdateMappingInput{}
		if in.Body.SiloUserID != nil {
			user, p := adminHistoryID(*in.Body.SiloUserID)
			if p != nil {
				return nil, p
			}
			input.SiloUserID = &user
		}
		if in.Body.SiloProfileID != nil {
			input.SiloProfileID = new(string(*in.Body.SiloProfileID))
		}
		row, err := s.UpdateMappingConditional(ctx, id, input, expected)
		return reg.adminHistoryMappingWriteResult(ctx, id, row, err)
	})
	Register(reg, op(http.MethodDelete, "/admin/history-imports/mappings/{id}", "deleteAdminHistoryImportMapping", true), func(ctx context.Context, in *AdminHistoryImportGuardedInput) (*struct{}, error) {
		s, p := reg.adminHistoryImports()
		if p != nil {
			return nil, p
		}
		id, p := adminHistoryID(in.ID)
		if p != nil {
			return nil, p
		}
		expected, p := reg.adminHistoryMappingVersion(ctx, id, in.IfMatch, in.IfNoneMatch)
		if p != nil {
			return nil, p
		}
		if err := s.DeleteMappingConditional(ctx, id, expected); err != nil {
			if errors.Is(err, historyimport.ErrStaleRevision) {
				_, err = reg.adminHistoryMappingWriteResult(ctx, id, nil, err)
				return nil, err
			}
			return nil, adminHistoryProblem(err)
		}
		return nil, nil
	})
}
