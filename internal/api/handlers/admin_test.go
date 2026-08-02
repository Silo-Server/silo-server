package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type adminUserContractRepo struct {
	users      map[int]*models.User
	nextID     int
	createCall *models.CreateUserInput
	updateID   int
	updateCall *models.UpdateUserInput
}

func newAdminUserContractRepo(users ...*models.User) *adminUserContractRepo {
	repo := &adminUserContractRepo{users: make(map[int]*models.User), nextID: 100}
	for _, user := range users {
		copy := *user
		repo.users[user.ID] = &copy
	}
	return repo
}

func (r *adminUserContractRepo) List(context.Context) ([]*models.User, error) {
	result := make([]*models.User, 0, len(r.users))
	for id := 1; id <= r.nextID; id++ {
		if user, ok := r.users[id]; ok {
			copy := *user
			result = append(result, &copy)
		}
	}
	return result, nil
}

func (r *adminUserContractRepo) Create(_ context.Context, input models.CreateUserInput) (*models.User, error) {
	copy := input
	r.createCall = &copy
	if r.collides(0, input.Username, input.Email) {
		return nil, fmt.Errorf("%w: users_identity_key", auth.ErrDuplicate)
	}
	r.nextID++
	user := &models.User{
		ID:       r.nextID,
		Username: input.Username,
		Email:    input.Email,
		Role:     input.Role,
		Enabled:  true,
	}
	r.users[user.ID] = user
	copyUser := *user
	return &copyUser, nil
}

func (r *adminUserContractRepo) Update(_ context.Context, id int, input models.UpdateUserInput) error {
	r.updateID = id
	copy := input
	r.updateCall = &copy
	current, ok := r.users[id]
	if !ok {
		return auth.ErrNotFound
	}
	username, email := current.Username, current.Email
	if input.Username != nil {
		username = *input.Username
	}
	if input.Email != nil {
		email = *input.Email
	}
	if r.collides(id, username, email) {
		return fmt.Errorf("%w: users_identity_key", auth.ErrDuplicate)
	}
	current.Username, current.Email = username, email
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if input.Password != nil {
		current.PasswordHash = "changed:" + *input.Password
	}
	return nil
}

func (r *adminUserContractRepo) Delete(_ context.Context, id int) error {
	delete(r.users, id)
	return nil
}

func (r *adminUserContractRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	copy := *user
	return &copy, nil
}

func (r *adminUserContractRepo) collides(exceptID int, username, email string) bool {
	username = auth.NormalizeUsername(username)
	email = auth.NormalizeEmail(email)
	for id, user := range r.users {
		if id == exceptID {
			continue
		}
		if strings.EqualFold(auth.NormalizeUsername(user.Username), username) ||
			strings.EqualFold(auth.NormalizeEmail(user.Email), email) {
			return true
		}
	}
	return false
}

func adminUserContractRequest(method, target string, id int, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if id == 0 {
		return req
	}
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", strconv.Itoa(id))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func decodeAdminUserContractResponse(t *testing.T, recorder *httptest.ResponseRecorder) adminUserResponse {
	t.Helper()
	var response adminUserResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	return response
}

func assertAdminUserIdentity(t *testing.T, got adminUserResponse, id int, username, email string, enabled bool) {
	t.Helper()
	if got.ID != id || got.Username != username || got.Email != email || got.Enabled != enabled {
		t.Fatalf("identity = {id:%d username:%q email:%q enabled:%v}, want {id:%d username:%q email:%q enabled:%v}",
			got.ID, got.Username, got.Email, got.Enabled, id, username, email, enabled)
	}
}

func TestAdminUserResponsesExposeNormalizedStableIdentity(t *testing.T) {
	repo := newAdminUserContractRepo(&models.User{
		ID: 7, Username: "  existing-user  ", Email: "  existing@example.com  ", Role: "user", Enabled: true,
	})
	handler := NewAdminHandler(repo, nil, nil)

	t.Run("list", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.HandleListUsers(recorder, adminUserContractRequest(http.MethodGet, "/admin/users", 0, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}
		var response []adminUserResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(response) != 1 {
			t.Fatalf("users = %d, want 1", len(response))
		}
		assertAdminUserIdentity(t, response[0], 7, "existing-user", "existing@example.com", true)
	})

	t.Run("get", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.HandleGetUser(recorder, adminUserContractRequest(http.MethodGet, "/admin/users/7", 7, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}
		assertAdminUserIdentity(t, decodeAdminUserContractResponse(t, recorder), 7, "existing-user", "existing@example.com", true)
	})

	t.Run("create", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		body := []byte(`{"username":"  new-user  ","email":"  new@example.com  ","password":"secret","role":"user"}`)
		handler.HandleCreateUser(recorder, adminUserContractRequest(http.MethodPost, "/admin/users", 0, body))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", recorder.Code, recorder.Body.String())
		}
		assertAdminUserIdentity(t, decodeAdminUserContractResponse(t, recorder), 101, "new-user", "new@example.com", true)
	})

	t.Run("update by numeric id", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		body := []byte(`{"username":"  renamed-user  ","email":"  renamed@example.com  ","password":"replacement","enabled":false}`)
		handler.HandleUpdateUser(recorder, adminUserContractRequest(http.MethodPut, "/admin/users/7", 7, body))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}
		assertAdminUserIdentity(t, decodeAdminUserContractResponse(t, recorder), 7, "renamed-user", "renamed@example.com", false)
		if repo.updateID != 7 || repo.updateCall == nil || repo.updateCall.Password == nil || *repo.updateCall.Password != "replacement" {
			t.Fatalf("update call = id %d input %#v, want numeric id 7 and password update", repo.updateID, repo.updateCall)
		}
		if repo.updateCall.Username == nil || *repo.updateCall.Username != "renamed-user" ||
			repo.updateCall.Email == nil || *repo.updateCall.Email != "renamed@example.com" {
			t.Fatalf("update identity was not normalized before repository call: %#v", repo.updateCall)
		}
	})
}

func TestAdminUserCreateCollisionReturnsConflictWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "normalized username", body: `{"username":"  ALICE  ","email":"different@example.com","password":"secret","role":"user"}`},
		{name: "normalized email", body: `{"username":"different","email":"  ALICE@EXAMPLE.COM  ","password":"secret","role":"user"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newAdminUserContractRepo(&models.User{ID: 1, Username: "alice", Email: "alice@example.com", Enabled: true})
			handler := NewAdminHandler(repo, nil, nil)
			recorder := httptest.NewRecorder()

			handler.HandleCreateUser(recorder, adminUserContractRequest(http.MethodPost, "/admin/users", 0, []byte(test.body)))

			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
			}
			var response errorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Error != "conflict" {
				t.Fatalf("error response = %#v, err=%v; want standard conflict", response, err)
			}
			if len(repo.users) != 1 || repo.users[1].Username != "alice" || repo.users[1].Email != "alice@example.com" {
				t.Fatalf("collision modified accounts: %#v", repo.users)
			}
		})
	}
}

func TestAdminUserUpdateCollisionReturnsConflictWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "normalized username", body: `{"username":"  ALICE  ","password":"replacement","enabled":false}`},
		{name: "normalized email", body: `{"email":"  ALICE@EXAMPLE.COM  ","password":"replacement","enabled":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newAdminUserContractRepo(
				&models.User{ID: 1, Username: "alice", Email: "alice@example.com", Enabled: true},
				&models.User{ID: 2, Username: "bob", Email: "bob@example.com", PasswordHash: "original", Enabled: true},
			)
			handler := NewAdminHandler(repo, nil, nil)
			recorder := httptest.NewRecorder()

			handler.HandleUpdateUser(recorder, adminUserContractRequest(http.MethodPut, "/admin/users/2", 2, []byte(test.body)))

			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
			}
			var response errorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Error != "conflict" {
				t.Fatalf("error response = %#v, err=%v; want standard conflict", response, err)
			}
			alice, bob := repo.users[1], repo.users[2]
			if alice.Username != "alice" || alice.Email != "alice@example.com" ||
				bob.Username != "bob" || bob.Email != "bob@example.com" || bob.PasswordHash != "original" || !bob.Enabled {
				t.Fatalf("collision modified accounts: alice=%#v bob=%#v", alice, bob)
			}
		})
	}
}

func TestUpdateRequiresSessionRevocation(t *testing.T) {
	role := "admin"
	sameRole := "user"
	enabled := true
	disabled := false
	libraryIDs := []int{1, 2}
	sameLibraryIDs := []int{1}
	emptyLibraryIDs := []int{}
	var allLibraryIDs []int
	maxPlaybackQuality := "1080p"
	sameMaxPlaybackQuality := "original"
	password := "new-password"
	username := "renamed"
	maxStreams := 4
	permissions := []string{"metadata_curation"}
	samePermissions := []string{"download"}
	groupID := int64(5)

	current := &models.User{
		Role:               "user",
		Permissions:        []string{"download"},
		Enabled:            false,
		LibraryIDs:         []int{1},
		MaxPlaybackQuality: "original",
	}

	tests := []struct {
		name string
		in   models.UpdateUserInput
		want bool
	}{
		{
			name: "permissions set",
			in:   models.UpdateUserInput{Permissions: &permissions},
			want: true,
		},
		{
			name: "permissions unchanged",
			in:   models.UpdateUserInput{Permissions: &samePermissions},
			want: false,
		},
		{
			name: "role",
			in:   models.UpdateUserInput{Role: &role},
			want: true,
		},
		{
			name: "role unchanged",
			in:   models.UpdateUserInput{Role: &sameRole},
			want: false,
		},
		{
			name: "enabled",
			in:   models.UpdateUserInput{Enabled: &enabled},
			want: true,
		},
		{
			name: "enabled unchanged",
			in:   models.UpdateUserInput{Enabled: &disabled},
			want: false,
		},
		{
			name: "library ids does not revoke session",
			in:   models.UpdateUserInput{LibraryIDs: &libraryIDs},
			want: false,
		},
		{
			name: "library ids unchanged",
			in:   models.UpdateUserInput{LibraryIDs: &sameLibraryIDs},
			want: false,
		},
		{
			name: "library ids nil does not revoke session",
			in:   models.UpdateUserInput{LibraryIDs: &allLibraryIDs},
			want: false,
		},
		{
			name: "max playback quality",
			in:   models.UpdateUserInput{MaxPlaybackQuality: &maxPlaybackQuality},
			want: true,
		},
		{
			name: "max playback quality unchanged",
			in:   models.UpdateUserInput{MaxPlaybackQuality: &sameMaxPlaybackQuality},
			want: false,
		},
		{
			name: "password",
			in:   models.UpdateUserInput{Password: &password},
			want: true,
		},
		{
			name: "access group set",
			in:   models.UpdateUserInput{AccessGroupIDSet: true, AccessGroupID: &groupID},
			want: true,
		},
		{
			name: "access group unchanged",
			in:   models.UpdateUserInput{AccessGroupIDSet: true, AccessGroupID: nil},
			want: false,
		},
		{
			name: "non access fields",
			in:   models.UpdateUserInput{Username: &username, MaxStreams: &maxStreams},
			want: false,
		},
		{
			name: "empty update",
			in:   models.UpdateUserInput{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateRequiresSessionRevocation(current, tt.in); got != tt.want {
				t.Fatalf("updateRequiresSessionRevocation() = %v, want %v", got, tt.want)
			}
		})
	}

	unrestrictedCurrent := *current
	unrestrictedCurrent.LibraryIDs = nil
	t.Run("library ids empty does not revoke session", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: &emptyLibraryIDs}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})

	t.Run("library ids nil unchanged", func(t *testing.T) {
		if got := updateRequiresSessionRevocation(&unrestrictedCurrent, models.UpdateUserInput{LibraryIDs: &allLibraryIDs}); got {
			t.Fatalf("updateRequiresSessionRevocation() = %v, want false", got)
		}
	})
}
