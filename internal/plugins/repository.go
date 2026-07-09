package plugins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRepositoryNotFound = errors.New("plugin repository not found")
var ErrManagedRepositoryReadOnly = errors.New("managed plugin repository is read-only")

type Repository struct {
	ID            int
	URL           string
	DisplayName   string
	Enabled       bool
	ManagedKey    *string
	SourceKind    string
	LastFetchedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CreateRepositoryInput struct {
	URL         string
	DisplayName string
	Enabled     *bool
}

type UpdateRepositoryInput struct {
	URL           *string
	DisplayName   *string
	Enabled       *bool
	LastFetchedAt *time.Time
}

type RepositoryStore struct {
	pool *pgxpool.Pool
}

func NewRepositoryStore(pool *pgxpool.Pool) *RepositoryStore {
	return &RepositoryStore{pool: pool}
}

const repositoryColumns = `id, url, display_name, enabled, managed_key, source_kind, last_fetched_at, created_at, updated_at`

func scanRepository(row pgx.Row) (*Repository, error) {
	var repository Repository
	var lastFetchedAt *time.Time
	if err := row.Scan(
		&repository.ID,
		&repository.URL,
		&repository.DisplayName,
		&repository.Enabled,
		&repository.ManagedKey,
		&repository.SourceKind,
		&lastFetchedAt,
		&repository.CreatedAt,
		&repository.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRepositoryNotFound
		}
		return nil, fmt.Errorf("scanning plugin repository: %w", err)
	}
	repository.LastFetchedAt = lastFetchedAt
	return &repository, nil
}

func scanRepositories(rows pgx.Rows) ([]*Repository, error) {
	var repositories []*Repository
	for rows.Next() {
		repository, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin repositories: %w", err)
	}
	return repositories, nil
}

func (s *RepositoryStore) Create(ctx context.Context, input CreateRepositoryInput) (*Repository, error) {
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	query := `INSERT INTO plugin_repositories (url, display_name, enabled)
		VALUES ($1, $2, $3)
		RETURNING ` + repositoryColumns

	return scanRepository(s.pool.QueryRow(ctx, query, input.URL, input.DisplayName, enabled))
}

func (s *RepositoryStore) GetByID(ctx context.Context, id int) (*Repository, error) {
	query := `SELECT ` + repositoryColumns + ` FROM plugin_repositories WHERE id = $1`
	return scanRepository(s.pool.QueryRow(ctx, query, id))
}

func (s *RepositoryStore) List(ctx context.Context) ([]*Repository, error) {
	query := `SELECT ` + repositoryColumns + ` FROM plugin_repositories ORDER BY id ASC`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing plugin repositories: %w", err)
	}
	defer rows.Close()
	return scanRepositories(rows)
}

func (s *RepositoryStore) Update(ctx context.Context, id int, input UpdateRepositoryInput) error {
	if input.URL != nil || input.DisplayName != nil || input.Enabled != nil {
		repository, err := s.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if repository.ManagedKey != nil {
			return ErrManagedRepositoryReadOnly
		}
	}

	var setClauses []string
	var args []any
	argIndex := 1

	if input.URL != nil {
		setClauses = append(setClauses, fmt.Sprintf("url = $%d", argIndex))
		args = append(args, *input.URL)
		argIndex++
	}
	if input.DisplayName != nil {
		setClauses = append(setClauses, fmt.Sprintf("display_name = $%d", argIndex))
		args = append(args, *input.DisplayName)
		argIndex++
	}
	if input.Enabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argIndex))
		args = append(args, *input.Enabled)
		argIndex++
	}
	if input.LastFetchedAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("last_fetched_at = $%d", argIndex))
		args = append(args, *input.LastFetchedAt)
		argIndex++
	}

	if len(setClauses) == 0 {
		_, err := s.GetByID(ctx, id)
		return err
	}

	setClauses = append(setClauses, "updated_at = NOW()")
	args = append(args, id)

	query := fmt.Sprintf(
		"UPDATE plugin_repositories SET %s WHERE id = $%d",
		strings.Join(setClauses, ", "),
		argIndex,
	)
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating plugin repository: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRepositoryNotFound
	}
	return nil
}

func (s *RepositoryStore) Delete(ctx context.Context, id int) error {
	repository, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if repository.ManagedKey != nil {
		return ErrManagedRepositoryReadOnly
	}

	tag, err := s.pool.Exec(ctx, `DELETE FROM plugin_repositories WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting plugin repository: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRepositoryNotFound
	}
	return nil
}
