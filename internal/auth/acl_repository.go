package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ACLRepository struct {
	db *pgxpool.Pool
}

func NewACLRepository(db *pgxpool.Pool) *ACLRepository {
	return &ACLRepository{db: db}
}

type aclRuleRow struct {
	ID           int64
	SubjectType  string
	SubjectID    string
	Action       string
	ResourceType string
	ResourceID   string
	Effect       string
	Conditions   []byte
	Priority     int
	Name         string
	Description  string
}

func (row aclRuleRow) toRule() (ACLRule, error) {
	conditions, err := decodeACLConditions(row.Conditions)
	if err != nil {
		return ACLRule{}, err
	}
	return ACLRule{
		ID:           row.ID,
		SubjectType:  ACLSubjectType(row.SubjectType),
		SubjectID:    row.SubjectID,
		Action:       ACLAction(row.Action),
		ResourceType: ACLResourceType(row.ResourceType),
		ResourceID:   row.ResourceID,
		Effect:       ACLEffect(row.Effect),
		Conditions:   conditions,
		Priority:     row.Priority,
		Name:         row.Name,
		Description:  row.Description,
	}, nil
}

func decodeACLConditions(raw []byte) (ACLCondition, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ACLCondition{}, nil
	}
	if trimmed[0] != '{' {
		return ACLCondition{}, fmt.Errorf("decoding acl conditions: expected object JSON, got %s", string(trimmed))
	}

	var conditions ACLCondition
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&conditions); err != nil {
		return ACLCondition{}, fmt.Errorf("decoding acl conditions: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ACLCondition{}, fmt.Errorf("decoding acl conditions: unexpected trailing JSON")
	}
	return conditions, nil
}

const aclRulesForUserQuery = `
		SELECT r.id, r.subject_type, r.subject_id, r.action, r.resource_type, r.resource_id, r.effect, r.conditions, r.priority, r.name, r.description
		FROM public.acl_rules r
		JOIN public.users u ON u.id = $1
		WHERE (r.subject_type = 'user' AND r.subject_id = u.id::text)
		   OR (r.subject_type = 'group' AND r.subject_id IN (
		       SELECT g.slug
		       FROM public.acl_groups g
		       JOIN public.acl_group_members gm ON gm.group_id = g.id
		       WHERE gm.user_id = u.id
		   ))
		   OR (r.subject_type = 'builtin_role' AND r.subject_id IN (
		       SELECT g.slug
		       FROM public.acl_groups g
		       JOIN public.acl_group_members gm ON gm.group_id = g.id
		       WHERE gm.user_id = u.id
		         AND g.built_in = true
		   ))
		   OR r.subject_type = 'everyone'
		ORDER BY r.priority DESC, r.id ASC
`

func (r *ACLRepository) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error) {
	rows, err := r.db.Query(ctx, aclRulesForUserQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []ACLRule{}
	for rows.Next() {
		var row aclRuleRow
		if err := rows.Scan(&row.ID, &row.SubjectType, &row.SubjectID, &row.Action, &row.ResourceType, &row.ResourceID, &row.Effect, &row.Conditions, &row.Priority, &row.Name, &row.Description); err != nil {
			return nil, err
		}
		rule, err := row.toRule()
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (r *ACLRepository) CurrentPolicyRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := r.db.QueryRow(ctx, `SELECT revision FROM public.acl_policy_revisions WHERE id = true`).Scan(&revision)
	return revision, err
}
