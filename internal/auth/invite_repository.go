package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for invite code operations.
var (
	ErrInviteCodeNotFound  = errors.New("invite code not found")
	ErrInviteCodeExhausted = errors.New("invite code has reached its maximum uses")
	ErrInviteCodeDisabled  = errors.New("invite code is disabled")
	ErrInviteCodeInvalid   = errors.New("invite code input is invalid")
)

// InviteCodeRepository provides CRUD operations for the invite_codes table.
type InviteCodeRepository struct {
	pool *pgxpool.Pool
}

// NewInviteCodeRepository creates a new InviteCodeRepository backed by the given pool.
func NewInviteCodeRepository(pool *pgxpool.Pool) *InviteCodeRepository {
	return &InviteCodeRepository{pool: pool}
}

const inviteCodeColumns = `id, code, label, max_uses, use_count, created_by, enabled, created_at, updated_at`

func scanInviteCode(row pgx.Row) (*models.InviteCode, error) {
	var ic models.InviteCode
	err := row.Scan(
		&ic.ID, &ic.Code, &ic.Label, &ic.MaxUses, &ic.UseCount,
		&ic.CreatedBy, &ic.Enabled, &ic.CreatedAt, &ic.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInviteCodeNotFound
		}
		return nil, fmt.Errorf("scanning invite code: %w", err)
	}
	return &ic, nil
}

// Create inserts a new invite code. If input.Code is empty, a random 8-char code is generated.
func (r *InviteCodeRepository) Create(ctx context.Context, input models.CreateInviteCodeInput) (*models.InviteCode, error) {
	code := input.Code
	if code == "" {
		var err error
		code, err = generateCode(8)
		if err != nil {
			return nil, fmt.Errorf("generating invite code: %w", err)
		}
	}

	query := `INSERT INTO invite_codes (code, label, max_uses, created_by)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + inviteCodeColumns

	row := r.pool.QueryRow(ctx, query, code, input.Label, input.MaxUses, input.CreatedBy)
	return scanInviteCode(row)
}

// GetByCode retrieves an invite code by its code string.
func (r *InviteCodeRepository) GetByCode(ctx context.Context, code string) (*models.InviteCode, error) {
	query := `SELECT ` + inviteCodeColumns + ` FROM invite_codes WHERE code = $1`
	return scanInviteCode(r.pool.QueryRow(ctx, query, code))
}

// GetByID retrieves an invite code by its numeric ID.
func (r *InviteCodeRepository) GetByID(ctx context.Context, id int) (*models.InviteCode, error) {
	query := `SELECT ` + inviteCodeColumns + ` FROM invite_codes WHERE id = $1`
	return scanInviteCode(r.pool.QueryRow(ctx, query, id))
}

// List returns all invite codes ordered by created_at descending.
func (r *InviteCodeRepository) List(ctx context.Context) ([]*models.InviteCode, error) {
	query := `SELECT ` + inviteCodeColumns + ` FROM invite_codes ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing invite codes: %w", err)
	}
	defer rows.Close()

	var codes []*models.InviteCode
	for rows.Next() {
		var ic models.InviteCode
		if err := rows.Scan(
			&ic.ID, &ic.Code, &ic.Label, &ic.MaxUses, &ic.UseCount,
			&ic.CreatedBy, &ic.Enabled, &ic.CreatedAt, &ic.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning invite code row: %w", err)
		}
		codes = append(codes, &ic)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating invite code rows: %w", err)
	}
	return codes, nil
}

// Update modifies an invite code's fields. Only non-nil fields in the input are updated.
func (r *InviteCodeRepository) Update(ctx context.Context, id int, input models.UpdateInviteCodeInput) error {
	setClauses := []string{}
	args := []any{}
	argIndex := 1

	if input.Label != nil {
		setClauses = append(setClauses, fmt.Sprintf("label = $%d", argIndex))
		args = append(args, *input.Label)
		argIndex++
	}
	if input.MaxUses != nil {
		setClauses = append(setClauses, fmt.Sprintf("max_uses = $%d", argIndex))
		args = append(args, *input.MaxUses)
		argIndex++
	}
	if input.Enabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argIndex))
		args = append(args, *input.Enabled)
		argIndex++
	}

	if len(setClauses) == 0 {
		_, err := r.GetByID(ctx, id)
		return err
	}

	setClauses = append(setClauses, "updated_at = NOW()")

	query := fmt.Sprintf("UPDATE invite_codes SET %s WHERE id = $%d",
		strings.Join(setClauses, ", "), argIndex)
	args = append(args, id)

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating invite code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInviteCodeNotFound
	}
	return nil
}

// TopUp atomically adds uses to an invite code and returns the updated row.
func (r *InviteCodeRepository) TopUp(ctx context.Context, id int, additionalUses int) (*models.InviteCode, error) {
	if additionalUses <= 0 {
		return nil, ErrInviteCodeInvalid
	}

	query := `UPDATE invite_codes
		SET max_uses = max_uses + $1, updated_at = NOW()
		WHERE id = $2
		RETURNING ` + inviteCodeColumns

	row := r.pool.QueryRow(ctx, query, additionalUses, id)
	return scanInviteCode(row)
}

// Delete removes an invite code by its ID.
func (r *InviteCodeRepository) Delete(ctx context.Context, id int) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM invite_codes WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("deleting invite code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInviteCodeNotFound
	}
	return nil
}

// RedeemCode atomically increments use_count for the given code.
// Returns ErrInviteCodeNotFound if the code doesn't exist,
// ErrInviteCodeExhausted if use_count >= max_uses, or
// ErrInviteCodeDisabled if the code is disabled.
func (r *InviteCodeRepository) RedeemCode(ctx context.Context, code string) error {
	return redeemCode(ctx, r.pool, code)
}

func redeemCode(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, code string) error {
	query := `UPDATE invite_codes
		SET use_count = use_count + 1, updated_at = NOW()
		WHERE code = $1 AND enabled = true AND use_count < max_uses`

	tag, err := db.Exec(ctx, query, code)
	if err != nil {
		return fmt.Errorf("redeeming invite code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Determine why: code doesn't exist, disabled, or exhausted.
		ic, err := scanInviteCode(db.QueryRow(ctx, `SELECT `+inviteCodeColumns+` FROM invite_codes WHERE code = $1`, code))
		if err != nil {
			return ErrInviteCodeNotFound
		}
		if !ic.Enabled {
			return ErrInviteCodeDisabled
		}
		if ic.UseCount >= ic.MaxUses {
			return ErrInviteCodeExhausted
		}
		return ErrInviteCodeNotFound
	}
	return nil
}

// generateCode generates a cryptographically random alphanumeric string of the given length.
func generateCode(length int) (string, error) {
	const charset = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no I/L/O/0/1 for readability
	result := make([]byte, length)
	for i := range result {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		result[i] = charset[idx.Int64()]
	}
	return string(result), nil
}
