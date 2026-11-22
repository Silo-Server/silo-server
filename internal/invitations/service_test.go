package invitations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
		if input.Email != "" && strings.EqualFold(row.Email, input.Email) && row.AcceptedAt == nil && row.RevokedAt == nil {
			now := time.Now()
			row.RevokedAt = &now
		}
	}
	f.nextID++
	inv := &models.Invitation{
		ID: f.nextID, Email: input.Email, Delivery: input.Delivery, TokenHash: tokenHash,
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

func (f *fakeRepo) AcceptAs(_ context.Context, hash string, linkAddress func() (string, error), provision func(*models.Invitation, pgx.Tx) (*models.User, error)) (*models.User, error) {
	row, ok := f.rows[hash]
	if !ok {
		return nil, ErrNotFound
	}
	if row.AcceptedAt != nil || row.RevokedAt != nil || !time.Now().Before(row.ExpiresAt) {
		return nil, ErrNotFound
	}
	if row.Email == "" {
		if linkAddress == nil {
			return nil, ErrEmailRequired
		}
		if _, err := linkAddress(); err != nil {
			return nil, err
		}
	}
	user, err := provision(row, nil)
	if err != nil {
		return nil, err
	}
	row.AcceptedAt, row.AcceptedUserID = new(time.Now()), new(int64(user.ID))
	if row.Email == "" {
		row.Email = user.Email
	}
	return user, nil
}

func (f *fakeRepo) RecordEmailOutcome(ctx context.Context, id int64, delivery string) error {
	row, err := f.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if row.Delivery == models.InvitationDeliveryEmailUnconfirmed {
		row.Delivery = delivery
	}
	return nil
}

func (f *fakeRepo) Resend(ctx context.Context, id int64, input models.CreateInvitationInput, hash string) (*models.Invitation, error) {
	prior, err := f.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if prior.AcceptedAt != nil || prior.RevokedAt != nil {
		return nil, ErrNotClaimable
	}
	if prior.Email == "" {
		now := time.Now()
		prior.RevokedAt = &now
	}
	return f.Create(ctx, input, hash)
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

func (f *fakeAccounts) CreateAccountInTransaction(_ context.Context, _ pgx.Tx, input auth.CreateAccountInput) (*models.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.created = append(f.created, input)
	return &models.User{ID: 100 + len(f.created), Username: input.User.Username, Email: input.User.Email}, nil
}

type fakeSessions struct {
	logins []string
	err    error
	// localLoginOff reports local password sign-in turned off.
	localLoginOff bool
}

func (f *fakeSessions) LocalPasswordLoginAllowed(context.Context) (bool, error) {
	return !f.localLoginOff, nil
}

func (f *fakeSessions) Login(_ context.Context, username, _, _, _ string) (*auth.TokenPair, *models.User, error) {
	f.logins = append(f.logins, username)
	if f.err != nil {
		return nil, nil, f.err
	}
	return &auth.TokenPair{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 900},
		&models.User{Username: username}, nil
}

type fakeMail struct {
	sent       []mail.Message
	configured bool
	err        error
	// loadErr models mail settings that cannot be read or are invalid:
	// Enabled reports false and Send returns the error.
	loadErr error
}

func (f *fakeMail) Enabled(context.Context) bool { return f.configured && f.loadErr == nil }

func (f *fakeMail) Send(_ context.Context, msg mail.Message) error {
	if f.loadErr != nil {
		return f.loadErr
	}
	if !f.configured {
		return mail.ErrNotConfigured
	}
	f.sent = append(f.sent, msg)
	return f.err
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

func TestSendAdminRoleRequiresOwnerInviter(t *testing.T) {
	users := adminInviter()
	users.byID[1].IsOwner = true
	regular := &models.User{ID: 5, Username: "pleb", Email: "pleb@example.com", Role: "user"}
	users.byID[5] = regular
	users.byEmail["pleb@example.com"] = regular
	admin := &models.User{ID: 6, Username: "second", Email: "second@example.com", Role: roleAdmin}
	users.byID[6] = admin
	users.byEmail["second@example.com"] = admin
	svc := newTestService(newFakeRepo(), users, &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	if _, err := svc.Send(context.Background(), SendInput{Email: "m@example.com", Role: roleAdmin, InvitedBy: 5}); !errors.Is(err, ErrRoleNotAllowed) {
		t.Errorf("non-admin minting admin: err = %v, want ErrRoleNotAllowed", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{Email: "m@example.com", Role: roleAdmin, InvitedBy: 6}); !errors.Is(err, ErrRoleNotAllowed) {
		t.Errorf("admin other than the owner minting admin: err = %v, want ErrRoleNotAllowed", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{Email: "m@example.com", Role: "user", InvitedBy: 6}); err != nil {
		t.Errorf("admin other than the owner inviting a user: %v", err)
	}
	groupID := int64(5)
	if _, err := svc.Send(context.Background(), SendInput{Email: "m2@example.com", Role: roleAdmin, AccessGroupID: &groupID, InvitedBy: 1}); !errors.Is(err, ErrAdminGrouped) {
		t.Errorf("admin invite with group: err = %v, want ErrAdminGrouped", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{Email: "m2@example.com", Role: roleAdmin, InvitedBy: 1}); err != nil {
		t.Errorf("owner minting admin: %v", err)
	}
	if _, err := svc.Send(context.Background(), SendInput{Email: "m3@example.com", Role: "root", InvitedBy: 1}); !errors.Is(err, ErrRoleNotAllowed) {
		t.Errorf("unknown role: err = %v, want ErrRoleNotAllowed", err)
	}
}

// With local password sign-in off the account an invitation creates could
// not sign in, so acceptance is refused and the invitation stays unspent.
func TestAcceptRefusedWhileLocalLoginIsOff(t *testing.T) {
	repo := newFakeRepo()
	accounts := &fakeAccounts{}
	sessions := &fakeSessions{}
	svc := newTestService(repo, adminInviter(), accounts, sessions, &fakeMail{configured: true}, fakeSettings{})
	sent, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sessions.localLoginOff = true
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite#")
	if _, _, err := svc.Accept(context.Background(), token, "", "hunter2hunter2", "d", ""); !errors.Is(err, auth.ErrLocalLoginDisabled) {
		t.Fatalf("accept = %v, want ErrLocalLoginDisabled", err)
	}
	if len(accounts.created) != 0 || len(sessions.logins) != 0 {
		t.Fatalf("created %d accounts, %d logins", len(accounts.created), len(sessions.logins))
	}
	sessions.localLoginOff = false
	if _, _, err := svc.Accept(context.Background(), token, "", "hunter2hunter2", "d", ""); err != nil {
		t.Fatalf("accept after local sign-in is back on: %v", err)
	}
}

func TestLookupHidesLifecycleDetail(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo, adminInviter(), &fakeAccounts{}, &fakeSessions{}, &fakeMail{configured: true}, fakeSettings{})

	sent, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite#")

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

func TestAcceptDoesNotLoginAfterProvisioningFailure(t *testing.T) {
	repo := newFakeRepo()
	sessions := &fakeSessions{}
	accounts := &fakeAccounts{err: auth.ErrTransactionalProfileUnavailable}
	svc := newTestService(repo, adminInviter(), accounts, sessions, &fakeMail{}, nil)
	sent, err := svc.Send(t.Context(), SendInput{Email: testInvitee, InvitedBy: 1, CreateProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite#")
	if _, _, err := svc.Accept(t.Context(), token, "", "test-password", "device", ""); !errors.Is(err, auth.ErrTransactionalProfileUnavailable) {
		t.Fatal(err)
	}
	if len(sessions.logins) != 0 || repo.rows[HashToken(token)].AcceptedAt != nil {
		t.Fatal("provisioning failure consumed invitation or issued session")
	}
}

func TestResendDeliveryErrorRetainsCommittedReplacement(t *testing.T) {
	repo := newFakeRepo()
	sender := &fakeMail{configured: true}
	svc := newTestService(repo, adminInviter(), &fakeAccounts{}, &fakeSessions{}, sender, nil)
	prior, err := svc.Send(t.Context(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	sender.err = errors.New("SMTP acknowledgement lost")
	replacement, err := svc.Resend(t.Context(), prior.Invitation.ID, 1, DeliveryDefault)
	if err == nil || replacement == nil || replacement.EmailSent || replacement.ClaimURL == "" {
		t.Fatalf("replacement=%v err=%v", replacement, err)
	}
	if prior.Invitation.RevokedAt == nil || replacement.Invitation.RevokedAt != nil || len(sender.sent) != 2 {
		t.Fatal("delivery error lost committed state")
	}
	if _, err := svc.Resend(t.Context(), prior.Invitation.ID, 1, DeliveryDefault); !errors.Is(err, ErrNotClaimable) {
		t.Fatalf("stale resend: %v", err)
	}
	if len(sender.sent) != 2 {
		t.Fatal("rejected resend sent email")
	}
}

func (f *fakeRepo) ListPage(context.Context, *PageKey, int) ([]*models.Invitation, bool, error) {
	return nil, false, nil
}

// An invitation can only be claimed with a local password, so none is sent
// while local password sign-in is off.
func TestSendRefusedWhileLocalLoginIsOff(t *testing.T) {
	repo := newFakeRepo()
	sender := &fakeMail{configured: true}
	svc := newTestService(repo, adminInviter(), &fakeAccounts{}, &fakeSessions{localLoginOff: true}, sender, fakeSettings{})
	if _, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1}); !errors.Is(err, auth.ErrLocalLoginDisabled) {
		t.Fatalf("Send = %v, want ErrLocalLoginDisabled", err)
	}
	if list, _ := svc.List(context.Background()); len(list) != 0 {
		t.Fatalf("stored %d invitations", len(list))
	}
}

func TestAcceptLinkInvitationUsesSignupEmailRule(t *testing.T) {
	svc := newTestService(newFakeRepo(), adminInviter(), &fakeAccounts{}, &fakeSessions{}, &fakeMail{}, fakeSettings{})
	sent, err := svc.Send(t.Context(), SendInput{Delivery: DeliveryLink, InvitedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite#")
	if _, _, err := svc.Accept(t.Context(), token, "sam@localhost", "hunter2hunter2", "d", ""); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("err = %v, want ErrInvalidEmail", err)
	}
}

func TestAcceptAppliesThePasswordPolicy(t *testing.T) {
	repo := newFakeRepo()
	accounts := &fakeAccounts{}
	sessions := &fakeSessions{}
	settings := fakeSettings{"auth.password_require_uppercase": "true"}
	svc := newTestService(repo, adminInviter(), accounts, sessions, &fakeMail{configured: true}, settings)
	sent, err := svc.Send(context.Background(), SendInput{Email: testInvitee, InvitedBy: 1})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	token := strings.TrimPrefix(sent.ClaimURL, "https://silo.example.com/invite#")
	if _, _, err := svc.Accept(context.Background(), token, "", "hunter2hunter2", "d", ""); !errors.Is(err, auth.ErrPasswordNeedsUppercase) {
		t.Fatalf("accept = %v, want ErrPasswordNeedsUppercase", err)
	}
	if len(accounts.created) != 0 {
		t.Fatalf("created %d accounts before the password passed", len(accounts.created))
	}
	if _, _, err := svc.Accept(context.Background(), token, "", "Hunter2hunter2", "d", ""); err != nil {
		t.Fatalf("accept with a policy-compliant password: %v", err)
	}
}
