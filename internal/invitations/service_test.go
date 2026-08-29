package invitations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
)

// --- fakes ---

type fakeRepo struct {
	rows   map[string]*models.Invitation // keyed by token hash
	nextID int64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rows: map[string]*models.Invitation{}}
}

func (f *fakeRepo) Create(_ context.Context, input models.CreateInvitationInput, tokenHash string) (*models.Invitation, error) {
	for _, row := range f.rows {
		if strings.EqualFold(row.Email, input.Email) && row.AcceptedAt == nil && row.RevokedAt == nil {
			now := time.Now()
			row.RevokedAt = &now
		}
	}
	f.nextID++
	inv := &models.Invitation{
		ID: f.nextID, Email: input.Email, TokenHash: tokenHash,
		Role: input.Role, AccessGroupID: input.AccessGroupID,
		LibraryIDs: input.LibraryIDs, CreateProfile: input.CreateProfile,
		ShowTour: input.ShowTour, Note: input.Note,
		InvitedBy: input.InvitedBy, ExpiresAt: input.ExpiresAt,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.rows[tokenHash] = inv
	return inv, nil
}

func (f *fakeRepo) GetByID(_ context.Context, id int64) (*models.Invitation, error) {
	for _, row := range f.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeRepo) GetByTokenHash(_ context.Context, hash string) (*models.Invitation, error) {
	if row, ok := f.rows[hash]; ok {
		return row, nil
	}
	return nil, ErrNotFound
}

func (f *fakeRepo) List(context.Context) ([]*models.Invitation, error) { return nil, nil }

func (f *fakeRepo) Accept(_ context.Context, hash string, userID int) error {
	row, ok := f.rows[hash]
	if !ok {
		return ErrNotFound
	}
	if row.AcceptedAt != nil || row.RevokedAt != nil || time.Now().After(row.ExpiresAt) {
		return ErrNotClaimable
	}
	now := time.Now()
	uid := int64(userID)
	row.AcceptedAt = &now
	row.AcceptedUserID = &uid
	return nil
}

func (f *fakeRepo) Revoke(_ context.Context, id int64) error {
	row, err := f.GetByID(context.Background(), id)
	if err != nil {
		return err
	}
	if row.AcceptedAt == nil && row.RevokedAt == nil {
		now := time.Now()
		row.RevokedAt = &now
	}
	return nil
}

func (f *fakeRepo) Delete(context.Context, int64) error { return nil }

type fakeUsers struct {
	byEmail map[string]*models.User
	byID    map[int]*models.User
}

func (f *fakeUsers) GetByEmail(_ context.Context, email string) (*models.User, error) {
	if u, ok := f.byEmail[strings.ToLower(email)]; ok {
		return u, nil
	}
	return nil, auth.ErrNotFound
}

func (f *fakeUsers) GetByUsername(_ context.Context, username string) (*models.User, error) {
	for _, u := range f.byEmail {
		if strings.EqualFold(u.Username, username) {
			return u, nil
		}
	}
	return nil, auth.ErrNotFound
}

func (f *fakeUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, auth.ErrNotFound
}

type fakeAccounts struct {
	created []auth.CreateAccountInput
	err     error
}

func (f *fakeAccounts) CreateAccount(_ context.Context, input auth.CreateAccountInput) (*models.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.created = append(f.created, input)
	return &models.User{ID: 100 + len(f.created), Username: input.User.Username, Email: input.User.Email}, nil
}

type fakeSessions struct{ logins []string }

func (f *fakeSessions) Login(_ context.Context, username, _, _, _ string) (*auth.TokenPair, *models.User, error) {
	f.logins = append(f.logins, username)
	return &auth.TokenPair{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 900},
		&models.User{Username: username}, nil
}

type fakeMail struct {
	sent       []mail.Message
	configured bool
}

func (f *fakeMail) Enabled(context.Context) bool { return f.configured }

func (f *fakeMail) Send(_ context.Context, msg mail.Message) error {
	if !f.configured {
		return mail.ErrNotConfigured
	}
	f.sent = append(f.sent, msg)
	return nil
}

type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }

func newTestService(repo *fakeRepo, users *fakeUsers, accounts *fakeAccounts, sessions *fakeSessions, sender *fakeMail, settings fakeSettings) *Service {
	return &Service{
		repo: repo, users: users, accounts: accounts, sessions: sessions,
		mail: sender, settings: settings,
		publicURL: "https://silo.example.com",
		ttl:       DefaultTTL,
		now:       time.Now,
	}
}

const testInvitee = "marco@example.com"

func adminInviter() *fakeUsers {
	admin := &models.User{ID: 1, Username: "quick", Email: "quick@example.com", Role: roleAdmin}
	return &fakeUsers{
		byEmail: map[string]*models.User{"quick@example.com": admin},
		byID:    map[int]*models.User{1: admin},
	}
}

// --- tests ---

func TestSendEmailsClaimLink(t *testing.T) {
	repo := newFakeRepo()
	sender := &fakeMail{configured: true}
	svc := newTestService(repo, adminInviter(), &fakeAccounts{}, &fakeSessions{}, sender, fakeSettings{})

	result, err := svc.Send(context.Background(), SendInput{
		Email: testInvitee, InvitedBy: 1, CreateProfile: true, ShowTour: true,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !result.EmailSent {
		t.Fatal("expected EmailSent")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(sender.sent))
	}
	msg := sender.sent[0]
	if msg.To[0] != testInvitee {
		t.Errorf("recipient = %q", msg.To[0])
	}
	if !strings.Contains(msg.TextBody, result.ClaimURL) {
		t.Error("text body missing claim URL")
	}
	if !strings.Contains(result.ClaimURL, "https://silo.example.com/invite/") {
		t.Errorf("claim URL = %q", result.ClaimURL)
	}
	// Raw token must not be stored: only its hash.
	token := strings.TrimPrefix(result.ClaimURL, "https://silo.example.com/invite/")
	if _, ok := repo.rows[token]; ok {
		t.Error("raw token stored in repository")
	}
	if _, ok := repo.rows[HashToken(token)]; !ok {
		t.Error("token hash not stored in repository")
	}
}

func TestSendWithoutMailReturnsClaimURL(t *testing.T) {
	svc := newTestService(newFakeRepo(), adminInviter(), &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: false}, fakeSettings{})

	result, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.EmailSent {
		t.Error("EmailSent should be false without SMTP")
	}
	if result.ClaimURL == "" {
		t.Error("ClaimURL must be returned for manual delivery")
	}
}

func TestSendRejectsExistingAccountAndBadInput(t *testing.T) {
	users := adminInviter()
	users.byEmail["taken@example.com"] = &models.User{ID: 2, Username: "taken", Email: "taken@example.com"}
	svc := newTestService(newFakeRepo(), users, &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	if _, err := svc.Send(context.Background(), SendInput{Email: "taken@example.com", InvitedBy: 1}); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("existing email: err = %v, want ErrEmailTaken", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{Email: "not-an-email", InvitedBy: 1}); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("bad address: err = %v, want ErrInvalidEmail", err)
	}
}

func TestSendAdminRoleRequiresAdminInviter(t *testing.T) {
	users := adminInviter()
	regular := &models.User{ID: 5, Username: "pleb", Email: "pleb@example.com", Role: "user"}
	users.byID[5] = regular
	users.byEmail["pleb@example.com"] = regular
	svc := newTestService(newFakeRepo(), users, &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	if _, err := svc.Send(context.Background(), SendInput{Email: "m@example.com", Role: roleAdmin, InvitedBy: 5}); !errors.Is(err, ErrRoleNotAllowed) {
		t.Errorf("non-admin minting admin: err = %v, want ErrRoleNotAllowed", err)
	}
	groupID := int64(5)
	if _, err := svc.Send(context.Background(), SendInput{Email: "m@example.com", Role: roleAdmin, AccessGroupID: &groupID, InvitedBy: 1}); !errors.Is(err, ErrAdminGrouped) {
		t.Errorf("admin invite with group: err = %v, want ErrAdminGrouped", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{Email: "m@example.com", Role: roleAdmin, InvitedBy: 1}); err != nil {
		t.Errorf("admin minting admin: %v", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{Email: "m2@example.com", Role: "root", InvitedBy: 1}); !errors.Is(err, ErrRoleNotAllowed) {
		t.Errorf("unknown role: err = %v, want ErrRoleNotAllowed", err)
	}
}

func TestResendInvalidatesOldToken(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo, adminInviter(), &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	first, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	oldToken := strings.TrimPrefix(first.ClaimURL, "https://silo.example.com/invite/")

	if _, err := svc.Resend(context.Background(), first.Invitation.ID, 1); err != nil {
		t.Fatalf("Resend: %v", err)
	}

	if _, err := svc.Lookup(context.Background(), oldToken); !errors.Is(err, ErrNotFound) {
		t.Errorf("old token after resend: err = %v, want ErrNotFound", err)
	}
}

func TestAcceptCreatesUserWithEmailAsUsername(t *testing.T) {
	repo := newFakeRepo()
	accounts := &fakeAccounts{}
	sessions := &fakeSessions{}
	groupID := int64(3)
	svc := newTestService(repo, adminInviter(), accounts, sessions, &fakeMail{configured: true}, fakeSettings{})

	sent, err := svc.Send(context.Background(), SendInput{
		Email: testInvitee, InvitedBy: 1,
		AccessGroupID: &groupID, LibraryIDs: []int{1, 2}, CreateProfile: true,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite/")

	pair, user, err := svc.Accept(context.Background(), token, "hunter2hunter2", "test-device", "127.0.0.1")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if pair == nil || user == nil {
		t.Fatal("Accept returned nil pair or user")
	}
	if len(accounts.created) != 1 {
		t.Fatalf("created %d accounts, want 1", len(accounts.created))
	}
	created := accounts.created[0]
	if created.User.Username != testInvitee || created.User.Email != testInvitee {
		t.Errorf("username/email = %q/%q, want email for both", created.User.Username, created.User.Email)
	}
	if created.User.AccessGroupID == nil || *created.User.AccessGroupID != groupID {
		t.Error("access group not carried onto account")
	}
	if len(created.User.LibraryIDs) != 2 {
		t.Error("library restriction not carried onto account")
	}
	if !created.DefaultProfile.Enabled || created.DefaultProfile.Name != "Marco" {
		t.Errorf("default profile = %+v, want enabled with name Marco", created.DefaultProfile)
	}
	if len(sessions.logins) != 1 {
		t.Error("expected a login after accept")
	}
}

func TestAcceptIsSingleUse(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo, adminInviter(), &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	sent, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite/")

	if _, _, err := svc.Accept(context.Background(), token, "hunter2hunter2", "d", ""); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if _, _, err := svc.Accept(context.Background(), token, "hunter2hunter2", "d", ""); err == nil {
		t.Fatal("second accept succeeded; invitation must be single-use")
	}
}

func TestAcceptRefusesExpired(t *testing.T) {
	repo := newFakeRepo()
	accounts := &fakeAccounts{}
	svc := newTestService(repo, adminInviter(), accounts, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	sent, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite/")
	repo.rows[HashToken(token)].ExpiresAt = time.Now().Add(-time.Hour)

	if _, _, err := svc.Accept(context.Background(), token, "hunter2hunter2", "d", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired accept: err = %v, want ErrNotFound", err)
	}
	if len(accounts.created) != 0 {
		t.Error("expired invitation must not create a user")
	}
}

func TestLookupHidesLifecycleDetail(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo, adminInviter(), &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	sent, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite/")

	if _, err := svc.Lookup(context.Background(), token); err != nil {
		t.Fatalf("pending lookup: %v", err)
	}
	if err := svc.Revoke(context.Background(), sent.Invitation.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Revoked, unknown, and garbage tokens must be indistinguishable.
	if _, err := svc.Lookup(context.Background(), token); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked lookup: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Lookup(context.Background(), "no-such-token"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown lookup: err = %v, want ErrNotFound", err)
	}
}

func TestLinkBasePrefersExternalURLSetting(t *testing.T) {
	svc := newTestService(newFakeRepo(), adminInviter(), &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true},
		fakeSettings{"notifications.email.external_url": "https://media.example.net/"})

	result, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.HasPrefix(result.ClaimURL, "https://media.example.net/invite/") {
		t.Errorf("claim URL = %q, want external_url base", result.ClaimURL)
	}
}

func TestProfileNameFromEmail(t *testing.T) {
	for input, want := range map[string]string{
		testInvitee:   "Marco",
		"m@x.io":      "M",
		"anna.k@x.io": "Anna.k",
	} {
		if got := profileNameFromEmail(input); got != want {
			t.Errorf("profileNameFromEmail(%q) = %q, want %q", input, got, want)
		}
	}
}
