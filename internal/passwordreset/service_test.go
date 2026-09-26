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
	// requested records IssueUnlessRecent calls; recent makes it find a
	// link younger than the cooldown.
	requested []requestedLink
	recent    bool
	withdrawn []string
}

type requestedLink struct {
	userID    int
	tokenHash string
	expiresAt time.Time
	minAge    time.Duration
}

func (f *fakeRepo) Issue(_ context.Context, _ int, tokenHash string, _ *int, _ time.Time) error {
	f.issued = append(f.issued, tokenHash)
	return nil
}

func (f *fakeRepo) Withdraw(_ context.Context, _ int, tokenHash string) error {
	f.withdrawn = append(f.withdrawn, tokenHash)
	return nil
}

func (f *fakeRepo) IssueUnlessRecent(_ context.Context, userID int, tokenHash string, expiresAt time.Time, minAge time.Duration) (bool, error) {
	f.requested = append(f.requested, requestedLink{userID, tokenHash, expiresAt, minAge})
	return !f.recent, nil
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

func (f fakeUsers) GetByUsername(_ context.Context, username string) (*models.User, error) {
	for _, u := range f {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, auth.ErrNotFound
}

func (f fakeUsers) GetByEmail(_ context.Context, email string) (*models.User, error) {
	for _, u := range f {
		if u.Email != "" && u.Email == email {
			return u, nil
		}
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
	s := &Service{repo: repo, users: users, sessions: fakeSessions{}, mail: sender, settings: settings, ttl: DefaultTTL, now: time.Now,
		pending: make(chan struct{}, maxPendingRequests), async: func(fn func()) { fn() }}
	return s
}

// selfServiceSettings turns self-service reset on for a server with a public URL.
func selfServiceSettings() fakeSettings {
	return fakeSettings{"server.public_url": "https://silo.example.test", SettingSelfService: "true"}
}

func TestSelfServiceNeedsTheSettingEmailAndALinkBase(t *testing.T) {
	for name, tc := range map[string]struct {
		settings            fakeSettings
		mailOn              bool
		enabled, configured bool
		requestErr          error
	}{
		"available":      {selfServiceSettings(), true, true, true, nil},
		"setting off":    {fakeSettings{"server.public_url": "https://silo.example.test"}, true, false, true, ErrSelfServiceDisabled},
		"mail off":       {selfServiceSettings(), false, true, false, ErrSelfServiceNotConfigured},
		"no public URL":  {fakeSettings{SettingSelfService: "true"}, true, true, false, ErrSelfServiceNotConfigured},
		"nothing set up": {fakeSettings{}, false, false, false, ErrSelfServiceNotConfigured},
	} {
		sender := &fakeMail{enabled: tc.mailOn}
		s := newTestService(&fakeRepo{}, sender, tc.settings)
		enabled, configured, err := s.SelfService(t.Context())
		if err != nil || enabled != tc.enabled || configured != tc.configured {
			t.Errorf("%s: SelfService = %v, %v, %v", name, enabled, configured, err)
		}
		if err := s.Request(t.Context(), "alice"); !errors.Is(err, tc.requestErr) {
			t.Errorf("%s: Request err = %v, want %v", name, err, tc.requestErr)
		}
		if tc.requestErr != nil && len(sender.sent) != 0 {
			t.Errorf("%s: sent %d emails while unavailable", name, len(sender.sent))
		}
	}
}

func TestRequestEmailsTheMatchingAccount(t *testing.T) {
	for _, login := range []string{"alice", " alice ", "alice@example.test"} {
		repo, sender := &fakeRepo{}, &fakeMail{enabled: true}
		s := newTestService(repo, sender, selfServiceSettings())
		before := time.Now()
		if err := s.Request(t.Context(), login); err != nil {
			t.Fatalf("%q: %v", login, err)
		}
		if len(repo.requested) != 1 || len(sender.sent) != 1 {
			t.Fatalf("%q: stored %d links, sent %d emails", login, len(repo.requested), len(sender.sent))
		}
		got, msg := repo.requested[0], sender.sent[0]
		if got.userID != 5 || got.minAge != selfServiceCooldown || got.expiresAt.Before(before.Add(SelfServiceTTL)) || got.expiresAt.After(time.Now().Add(SelfServiceTTL)) {
			t.Fatalf("%q: stored %+v", login, got)
		}
		_, token, ok := strings.Cut(msg.TextBody, "https://silo.example.test/reset-password/")
		token, _, _ = strings.Cut(token, "\n")
		if !ok || msg.To[0] != "alice@example.test" || auth.HashLinkToken(token) != got.tokenHash {
			t.Fatalf("%q: email to %v does not carry the stored link", login, msg.To)
		}
		// The account holder asked; the email must not credit an admin.
		if !strings.Contains(msg.TextBody, "Someone asked to reset the password") || strings.Contains(msg.TextBody, "An admin") {
			t.Fatalf("%q: wording %q", login, msg.TextBody)
		}
	}
}

func TestRequestIsSilentWhenNothingShouldBeSent(t *testing.T) {
	for name, login := range map[string]string{
		"unknown account":   "ghost",
		"unknown address":   "ghost@example.test",
		"external provider": "sso",
		"disabled":          "off",
		"no email":          "nomail",
	} {
		repo, sender := &fakeRepo{}, &fakeMail{enabled: true}
		s := newTestService(repo, sender, selfServiceSettings())
		if err := s.Request(t.Context(), login); err != nil {
			t.Errorf("%s: answered differently: %v", name, err)
		}
		if len(repo.requested) != 0 || len(sender.sent) != 0 {
			t.Errorf("%s: stored %d links, sent %d emails", name, len(repo.requested), len(sender.sent))
		}
	}

	// A link sent moments ago is neither replaced nor sent again.
	repo, sender := &fakeRepo{recent: true}, &fakeMail{enabled: true}
	s := newTestService(repo, sender, selfServiceSettings())
	if err := s.Request(t.Context(), "alice"); err != nil || len(repo.requested) != 1 || len(sender.sent) != 0 {
		t.Fatalf("cooldown: %v, stored %d, sent %d", err, len(repo.requested), len(sender.sent))
	}

	// A failed send is logged, not reported: the caller already has its
	// answer. A link that certainly was not sent is withdrawn so asking again
	// works now; one whose delivery is uncertain keeps its cooldown.
	repo, sender = &fakeRepo{}, &fakeMail{enabled: true, err: errors.Join(mail.ErrNotSent, errors.New("dial refused"))}
	if err := newTestService(repo, sender, selfServiceSettings()).Request(t.Context(), "alice"); err != nil || len(sender.sent) != 1 {
		t.Fatalf("failed send: %v, attempted %d", err, len(sender.sent))
	}
	if len(repo.withdrawn) != 1 || repo.withdrawn[0] != repo.requested[0].tokenHash {
		t.Fatalf("undelivered link withdrawn %q, stored %+v", repo.withdrawn, repo.requested)
	}
	repo, sender = &fakeRepo{}, &fakeMail{enabled: true, err: errors.New("smtp send: timeout after DATA")}
	if err := newTestService(repo, sender, selfServiceSettings()).Request(t.Context(), "alice"); err != nil || len(repo.withdrawn) != 0 {
		t.Fatalf("uncertain send: %v, withdrawn %q", err, repo.withdrawn)
	}

	// A panic in the background work is contained and frees its slot.
	panicky := newTestService(&fakeRepo{}, &fakeMail{enabled: true}, selfServiceSettings())
	panicky.users = nil
	if err := panicky.Request(t.Context(), "alice"); err != nil || len(panicky.pending) != 0 {
		t.Fatalf("panicking request: %v, %d slots held", err, len(panicky.pending))
	}
}

func TestRequestDropsWorkBeyondThePendingBound(t *testing.T) {
	repo, sender := &fakeRepo{}, &fakeMail{enabled: true}
	s := newTestService(repo, sender, selfServiceSettings())
	var queued []func()
	s.async = func(fn func()) { queued = append(queued, fn) }
	for range maxPendingRequests + 2 {
		if err := s.Request(t.Context(), "alice"); err != nil {
			t.Fatal(err)
		}
	}
	if len(queued) != maxPendingRequests {
		t.Fatalf("started %d background requests, want %d", len(queued), maxPendingRequests)
	}
	// Finishing one frees its slot.
	queued[0]()
	if err := s.Request(t.Context(), "alice"); err != nil || len(queued) != maxPendingRequests+1 {
		t.Fatalf("after one finished: %v, started %d", err, len(queued))
	}
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
