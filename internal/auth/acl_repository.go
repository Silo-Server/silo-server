package auth

import (
	"context"
	"encoding/json"

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

func (row aclRuleRow) toRule() ACLRule {
	var conditions ACLCondition
	if len(row.Conditions) > 0 {
		_ = json.Unmarshal(row.Conditions, &conditions)
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
	}
}

func (r *ACLRepository) ListRulesForUser(ctx context.Context, userID int) ([]ACLRule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, subject_type, subject_id, action, resource_type, resource_id, effect, conditions, priority, name, description
		FROM public.acl_rules
		WHERE (subject_type = 'user' AND subject_id = $1::text)
		   OR (subject_type = 'group' AND subject_id IN (
		       SELECT g.slug
		       FROM public.acl_groups g
		       JOIN public.acl_group_members gm ON gm.group_id = g.id
		       WHERE gm.user_id = $2
		   ))
		   OR subject_type = 'everyone'
		ORDER BY priority DESC, id ASC
	`, userID, userID)
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
		rules = append(rules, row.toRule())
	}
	return rules, rows.Err()
}

func (r *ACLRepository) CurrentPolicyRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := r.db.QueryRow(ctx, `SELECT revision FROM public.acl_policy_revisions WHERE id = true`).Scan(&revision)
	return revision, err
}
