package passwordreset

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

type fakeRepo struct {
	issued    []string
	completed []string
	user      *models.User
}

func (f *fakeRepo) Issue(_ context.Context, _ int, tokenHash string, _ *int, _ time.Time) error {
	f.issued = append(f.issued, tokenHash)
	return nil
}

func (f *fakeRepo) Lookup(context.Context, string) (*Link, error) { return nil, ErrNotFound }

func (f *fakeRepo) Complete(_ context.Context, tokenHash, _ string) (*models.User, error) {
	f.completed = append(f.completed, tokenHash)
	return f.user, nil
}

type fakeUsers map[int]*models.User

func (f fakeUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if u, ok := f[id]; ok {
		return u, nil
	}
	return nil, auth.ErrNotFound
}

type fakeMail struct {
	enabled bool
	err     error
	sent    []mail.Message
}

func (f *fakeMail) Enabled(context.Context) bool { return f.enabled }
func (f *fakeMail) Send(_ context.Context, msg mail.Message) error {
	f.sent = append(f.sent, msg)
	return f.err
}

type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }

type fakeSessions struct{ err error }

func (f fakeSessions) Login(context.Context, string, string, string, string) (*auth.TokenPair, *models.User, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return &auth.TokenPair{AccessToken: "access"}, &models.User{ID: 5}, nil
}

func newTestService(repo *fakeRepo, sender *fakeMail, settings fakeSettings) *Service {
	users := fakeUsers{
		5: {ID: 5, Username: "alice", Email: "alice@example.test", PasswordHash: "hash", LocalPasswordLoginEnabled: true, Enabled: true},
		6: {ID: 6, Username: "sso", Email: "sso@example.test", PasswordHash: "hash", Enabled: true},
		7: {ID: 7, Username: "off", Email: "off@example.test", PasswordHash: "hash", LocalPasswordLoginEnabled: true},
		8: {ID: 8, Username: "nomail", PasswordHash: "hash", LocalPasswordLoginEnabled: true, Enabled: true},
	}
	s := &Service{repo: repo, users: users, sessions: fakeSessions{}, mail: sender, settings: settings, ttl: DefaultTTL, now: time.Now}
	return s
}

func TestIssueDeliversLinkOnlyWhereAsked(t *testing.T) {
	repo, sender := &fakeRepo{}, &fakeMail{enabled: true}
	s := newTestService(repo, sender, fakeSettings{"server.public_url": "https://silo.example.test/"})

	link, err := s.Issue(t.Context(), IssueInput{UserID: 5, IssuedBy: 1, Delivery: DeliveryLink})
	if err != nil || !strings.HasPrefix(link.URL, "https://silo.example.test/reset-password/") || len(sender.sent) != 0 {
		t.Fatalf("link = %+v, %v, sent %d", link, err, len(sender.sent))
	}
	token := strings.TrimPrefix(link.URL, "https://silo.example.test/reset-password/")
	if repo.issued[0] != auth.HashLinkToken(token) {
		t.Fatal("stored digest does not match the returned link")
	}

	emailed, err := s.Issue(t.Context(), IssueInput{UserID: 5, IssuedBy: 1, Delivery: DeliveryEmail})
	if err != nil || !emailed.EmailSent || emailed.URL != "" || len(sender.sent) != 1 || sender.sent[0].To[0] != "alice@example.test" {
		t.Fatalf("email = %+v, %v", emailed, err)
	}
	if !strings.Contains(sender.sent[0].TextBody, "/reset-password/") {
		t.Fatal("email carries no link")
	}

	sender.err = errors.New("smtp timeout")
	uncertain, err := s.Issue(t.Context(), IssueInput{UserID: 5, Delivery: DeliveryEmail})
	if err == nil || uncertain == nil || uncertain.EmailSent {
		t.Fatalf("failed delivery = %+v, %v", uncertain, err)
	}
}

func TestIssueRefusesBeforeMinting(t *testing.T) {
	repo := &fakeRepo{}
	s := newTestService(repo, &fakeMail{}, fakeSettings{"server.public_url": "https://silo.example.test"})
	for name, tc := range map[string]struct {
		in   IssueInput
		want error
	}{
		"external provider": {IssueInput{UserID: 6, Delivery: DeliveryLink}, auth.ErrPasswordLoginDisabled},
		"disabled":          {IssueInput{UserID: 7, Delivery: DeliveryLink}, ErrAccountDisabled},
		"no email":          {IssueInput{UserID: 8, Delivery: DeliveryEmail}, ErrNoEmail},
		"mail off":          {IssueInput{UserID: 5, Delivery: DeliveryEmail}, mail.ErrNotConfigured},
		"unknown account":   {IssueInput{UserID: 99, Delivery: DeliveryLink}, auth.ErrNotFound},
		"unknown delivery":  {IssueInput{UserID: 5, Delivery: "sms"}, ErrUnknownDelivery},
	} {
		if _, err := s.Issue(t.Context(), tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	noBase := newTestService(repo, &fakeMail{enabled: true}, fakeSettings{})
	if _, err := noBase.Issue(t.Context(), IssueInput{UserID: 5, Delivery: DeliveryLink}); !errors.Is(err, ErrNoLinkBase) {
		t.Errorf("no link base: %v", err)
	}
	if len(repo.issued) != 0 {
		t.Fatalf("a refused request replaced the account's link %d times", len(repo.issued))
	}
	if caps := noBase.Capabilities(t.Context()); caps.Link || caps.Email {
		t.Fatalf("capabilities without a public URL = %+v", caps)
	}
}

func TestCompleteValidatesFirstAndReportsCommittedReset(t *testing.T) {
	repo := &fakeRepo{user: &models.User{ID: 5, Username: "alice"}}
	s := newTestService(repo, &fakeMail{}, fakeSettings{})
	var revoked []int
	s.OnSessionsRevoked(func(_ context.Context, id int) { revoked = append(revoked, id) })

	if _, _, err := s.Complete(t.Context(), "tok", "short", "", ""); !errors.Is(err, auth.ErrPasswordTooShort) || len(repo.completed) != 0 {
		t.Fatalf("short password: %v, spent %d links", err, len(repo.completed))
	}
	if _, _, err := s.Complete(t.Context(), " ", "long-enough", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blank token: %v", err)
	}
	pair, _, err := s.Complete(t.Context(), "tok", "long-enough", "", "")
	if err != nil || pair == nil || len(revoked) != 1 || revoked[0] != 5 {
		t.Fatalf("complete = %v, revoked %v", err, revoked)
	}

	s.sessions = fakeSessions{err: errors.New("login down")}
	pair, user, err := s.Complete(t.Context(), "tok", "long-enough", "", "")
	if !errors.Is(err, ErrSessionStart) || pair != nil || user == nil {
		t.Fatalf("sign-in failure after commit = %v, %v, %v", pair, user, err)
	}
}
