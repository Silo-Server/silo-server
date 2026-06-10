package notifications

import "time"

type Category string

const (
	CategoryRequest      Category = "request"
	CategoryContent      Category = "content"
	CategoryAnnouncement Category = "announcement"
	CategorySystem       Category = "system"
	CategoryAdmin        Category = "admin"
	// CategoryContentDigest is a preference key only; digest rows are stored
	// with CategoryContent and type "content.digest".
	CategoryContentDigest Category = "content_digest"
)

// MutableCategories are valid notification_preferences rows.
var MutableCategories = []Category{
	CategoryRequest, CategoryContent, CategorySystem, CategoryAdmin, CategoryContentDigest,
}

type Notification struct {
	ID          int64      `json:"id"`
	UserID      int        `json:"user_id"`
	ProfileID   *string    `json:"profile_id,omitempty"`
	Category    Category   `json:"category"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	Link        *string    `json:"link,omitempty"`
	ItemID      *string    `json:"item_id,omitempty"`
	SourceEvent string     `json:"-"`
	DedupRef    string     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	DismissedAt *time.Time `json:"-"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

type Preference struct {
	Category Category `json:"category"`
	Enabled  bool     `json:"enabled"`
}

type Announcement struct {
	ID        int64      `json:"id"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Audience  Audience   `json:"audience"`
	CreatedBy *int       `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Audience: exactly one field set.
type Audience struct {
	All        bool  `json:"all,omitempty"`
	UserIDs    []int `json:"user_ids,omitempty"`
	LibraryIDs []int `json:"library_ids,omitempty"`
}

type ListFilter struct {
	UserID     int
	ProfileID  string // active profile; matches profile_id IS NULL OR profile_id = this
	ChildSafe  bool   // true => exclude request/system/admin categories
	UnreadOnly bool
	Category   Category // optional
	Cursor     int64    // created-before id cursor; 0 = first page
	Limit      int
}
