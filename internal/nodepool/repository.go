package nodepool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// NodeTypeProxy identifies a proxy stream node.
	NodeTypeProxy = "proxy"
	// NodeTypeTranscode identifies a transcode stream node.
	NodeTypeTranscode = "transcode"
)

// Node represents a stream node in the database.
type Node struct {
	ID               int        `json:"id"`
	Name             string     `json:"name"`
	Type             string     `json:"type"`
	URL              string     `json:"url"`
	Enabled          bool       `json:"enabled"`
	Healthy          bool       `json:"healthy"`
	ActiveJobs       int        `json:"active_jobs"`
	Group            *string    `json:"group"`              // co-location group; nil = ungrouped
	MaxJobs          *int       `json:"max_jobs"`           // concurrent job cap; nil = unlimited
	MaxBandwidthKbps *int       `json:"max_bandwidth_kbps"` // egress cap in kilobits/s; nil = unlimited
	EgressKbps       int        `json:"egress_kbps"`        // health-reported rolling egress average
	LastHealthCheck  *time.Time `json:"last_health_check"`
	CreatedAt        time.Time  `json:"created_at"`
}

// CreateNodeInput holds the fields for creating a new node.
type CreateNodeInput struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	URL              string `json:"url"`
	Group            string `json:"group"`              // empty = ungrouped
	MaxJobs          *int   `json:"max_jobs"`           // nil or <= 0 = unlimited
	MaxBandwidthKbps *int   `json:"max_bandwidth_kbps"` // nil or <= 0 = unlimited
}

// Validate checks required fields and allowed values.
func (i CreateNodeInput) Validate() error {
	if i.Name == "" {
		return errors.New("name is required")
	}
	if i.Type != NodeTypeProxy && i.Type != NodeTypeTranscode {
		return fmt.Errorf("type must be %q or %q", NodeTypeProxy, NodeTypeTranscode)
	}
	if i.URL == "" {
		return errors.New("url is required")
	}
	return nil
}

// UpdateNodeInput holds the fields for updating a node.
// The optional fields distinguish "leave unchanged" (nil) from "clear":
// an empty-string Group clears the group, and a non-positive MaxJobs or
// MaxBandwidthKbps clears that cap.
type UpdateNodeInput struct {
	Name             *string `json:"name,omitempty"`
	URL              *string `json:"url,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	Group            *string `json:"group,omitempty"`
	MaxJobs          *int    `json:"max_jobs,omitempty"`
	MaxBandwidthKbps *int    `json:"max_bandwidth_kbps,omitempty"`
}

// normalizeGroup trims a group label and converts empty to NULL.
func normalizeGroup(group string) *string {
	g := strings.TrimSpace(group)
	if g == "" {
		return nil
	}
	return &g
}

// normalizeCap converts non-positive capacity values to NULL (unlimited).
func normalizeCap(v *int) *int {
	if v == nil || *v <= 0 {
		return nil
	}
	return v
}

// Repository provides CRUD operations for stream nodes.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new node repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const nodeColumns = `id, name, type, url, enabled, healthy, active_jobs, node_group, max_jobs, max_bandwidth_kbps, egress_kbps, last_health_check, created_at`

func scanNode(row pgx.Row) (*Node, error) {
	var n Node
	err := row.Scan(
		&n.ID, &n.Name, &n.Type, &n.URL,
		&n.Enabled, &n.Healthy, &n.ActiveJobs,
		&n.Group, &n.MaxJobs,
		&n.MaxBandwidthKbps, &n.EgressKbps,
		&n.LastHealthCheck, &n.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func scanNodes(rows pgx.Rows) ([]*Node, error) {
	var nodes []*Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(
			&n.ID, &n.Name, &n.Type, &n.URL,
			&n.Enabled, &n.Healthy, &n.ActiveJobs,
			&n.Group, &n.MaxJobs,
			&n.MaxBandwidthKbps, &n.EgressKbps,
			&n.LastHealthCheck, &n.CreatedAt,
		); err != nil {
			return nil, err
		}
		nodes = append(nodes, &n)
	}
	return nodes, rows.Err()
}

// List returns all nodes ordered by type then name.
func (r *Repository) List(ctx context.Context) ([]*Node, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+nodeColumns+` FROM stream_nodes ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// ListEnabled returns all enabled nodes of a given type.
func (r *Repository) ListEnabled(ctx context.Context, nodeType string) ([]*Node, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+nodeColumns+` FROM stream_nodes WHERE type = $1 AND enabled = true ORDER BY name`,
		nodeType)
	if err != nil {
		return nil, fmt.Errorf("list enabled nodes: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// GetByID returns a single node by ID.
func (r *Repository) GetByID(ctx context.Context, id int) (*Node, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+nodeColumns+` FROM stream_nodes WHERE id = $1`, id)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	return n, nil
}

// Create inserts a new node and returns it.
func (r *Repository) Create(ctx context.Context, input CreateNodeInput) (*Node, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO stream_nodes (name, type, url, node_group, max_jobs, max_bandwidth_kbps)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+nodeColumns,
		input.Name, input.Type, input.URL, normalizeGroup(input.Group),
		normalizeCap(input.MaxJobs), normalizeCap(input.MaxBandwidthKbps))
	return scanNode(row)
}

// Update modifies a node's mutable fields. The optional fields use sentinel
// values to clear: an empty-string group and non-positive caps set the
// column to NULL (see UpdateNodeInput).
func (r *Repository) Update(ctx context.Context, id int, input UpdateNodeInput) (*Node, error) {
	var group *string
	if input.Group != nil {
		group = normalizeGroup(*input.Group)
	}
	var maxJobs, maxBandwidth *int
	if input.MaxJobs != nil {
		maxJobs = normalizeCap(input.MaxJobs)
	}
	if input.MaxBandwidthKbps != nil {
		maxBandwidth = normalizeCap(input.MaxBandwidthKbps)
	}
	row := r.pool.QueryRow(ctx,
		`UPDATE stream_nodes SET
			name = COALESCE($2, name),
			url = COALESCE($3, url),
			enabled = COALESCE($4, enabled),
			node_group = CASE WHEN $5::boolean THEN $6::text ELSE node_group END,
			max_jobs = CASE WHEN $7::boolean THEN $8::integer ELSE max_jobs END,
			max_bandwidth_kbps = CASE WHEN $9::boolean THEN $10::integer ELSE max_bandwidth_kbps END
		 WHERE id = $1
		 RETURNING `+nodeColumns,
		id, input.Name, input.URL, input.Enabled,
		input.Group != nil, group,
		input.MaxJobs != nil, maxJobs,
		input.MaxBandwidthKbps != nil, maxBandwidth)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update node: %w", err)
	}
	return n, nil
}

// Delete removes a node by ID.
func (r *Repository) Delete(ctx context.Context, id int) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM stream_nodes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// UpdateHealth updates a node's health status, active job count, and
// reported egress bandwidth.
func (r *Repository) UpdateHealth(ctx context.Context, id int, healthy bool, activeJobs, egressKbps int) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE stream_nodes SET healthy = $2, active_jobs = $3, egress_kbps = $4, last_health_check = NOW()
		 WHERE id = $1`,
		id, healthy, activeJobs, egressKbps)
	if err != nil {
		return fmt.Errorf("update node health: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// Sentinel errors.
var ErrNodeNotFound = errors.New("stream node not found")
