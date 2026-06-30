package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ACLRepository struct {
	db *pgxpool.Pool
}

var ErrUnknownACLGroup = errors.New("unknown ACL group")
var ErrACLGroupExists = errors.New("acl group exists")
var ErrProtectedACLGroup = errors.New("protected acl group")

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

func decodeACLPolicy(raw []byte) (ACLPolicy, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ACLPolicy{}, nil
	}
	if trimmed[0] != '{' {
		return ACLPolicy{}, fmt.Errorf("decoding acl policy: expected object JSON, got %s", string(trimmed))
	}

	var policy ACLPolicy
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return ACLPolicy{}, fmt.Errorf("decoding acl policy: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ACLPolicy{}, fmt.Errorf("decoding acl policy: unexpected trailing JSON")
	}
	normalized, err := NormalizeACLPolicy(policy)
	if err != nil {
		return ACLPolicy{}, err
	}
	return normalized, nil
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

const aclPoliciesForUserQuery = `
		SELECT g.policy
		FROM public.acl_groups g
		JOIN public.acl_group_members gm ON gm.group_id = g.id
		WHERE gm.user_id = $1
		ORDER BY g.id ASC
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

func (r *ACLRepository) ListPoliciesForUser(ctx context.Context, userID int) ([]ACLPolicy, error) {
	rows, err := r.db.Query(ctx, aclPoliciesForUserQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	policies := []ACLPolicy{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		policy, err := decodeACLPolicy(raw)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (r *ACLRepository) CurrentPolicyRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := r.db.QueryRow(ctx, `SELECT revision FROM public.acl_policy_revisions WHERE id = true`).Scan(&revision)
	return revision, err
}

func (r *ACLRepository) ListGroups(ctx context.Context) ([]ACLGroup, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, slug, name, description, policy, built_in, protected, created_at, updated_at
		FROM public.acl_groups
		ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing ACL groups: %w", err)
	}
	defer rows.Close()

	groups := []ACLGroup{}
	for rows.Next() {
		var group ACLGroup
		var rawPolicy []byte
		if err := rows.Scan(&group.ID, &group.Slug, &group.Name, &group.Description, &rawPolicy, &group.BuiltIn, &group.Protected, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning ACL group: %w", err)
		}
		policy, err := decodeACLPolicy(rawPolicy)
		if err != nil {
			return nil, err
		}
		group.Policy = policy
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ACL groups: %w", err)
	}
	return groups, nil
}

func (r *ACLRepository) GetGroup(ctx context.Context, slug string) (ACLGroup, []ACLRule, error) {
	normalizedSlug, err := NormalizeACLGroupSlug(slug)
	if err != nil {
		return ACLGroup{}, nil, err
	}

	var group ACLGroup
	var rawPolicy []byte
	err = r.db.QueryRow(ctx, `
		SELECT id, slug, name, description, policy, built_in, protected, created_at, updated_at
		FROM public.acl_groups
		WHERE slug = $1`, normalizedSlug).
		Scan(&group.ID, &group.Slug, &group.Name, &group.Description, &rawPolicy, &group.BuiltIn, &group.Protected, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ACLGroup{}, nil, ErrNotFound
		}
		return ACLGroup{}, nil, fmt.Errorf("loading ACL group: %w", err)
	}
	policy, err := decodeACLPolicy(rawPolicy)
	if err != nil {
		return ACLGroup{}, nil, err
	}
	group.Policy = policy

	rules, err := r.listRulesForGroup(ctx, group.Slug)
	if err != nil {
		return ACLGroup{}, nil, err
	}
	return group, rules, nil
}

func (r *ACLRepository) CreateGroup(ctx context.Context, input CreateACLGroupInput) (ACLGroup, error) {
	slug, err := NormalizeACLGroupSlug(input.Slug)
	if err != nil {
		return ACLGroup{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ACLGroup{}, fmt.Errorf("ACL group name is required")
	}
	description := strings.TrimSpace(input.Description)
	policy, err := NormalizeACLPolicy(input.Policy)
	if err != nil {
		return ACLGroup{}, err
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return ACLGroup{}, fmt.Errorf("encoding ACL group policy: %w", err)
	}

	var group ACLGroup
	var rawPolicy []byte
	err = r.db.QueryRow(ctx, `
		INSERT INTO public.acl_groups (slug, name, description, policy, built_in, protected)
		VALUES ($1, $2, $3, $4, false, false)
		RETURNING id, slug, name, description, policy, built_in, protected, created_at, updated_at`,
		slug, name, description, policyJSON).
		Scan(&group.ID, &group.Slug, &group.Name, &group.Description, &rawPolicy, &group.BuiltIn, &group.Protected, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ACLGroup{}, ErrACLGroupExists
		}
		return ACLGroup{}, fmt.Errorf("creating ACL group: %w", err)
	}
	group.Policy = policy
	if err := r.bumpPolicyRevision(ctx); err != nil {
		return ACLGroup{}, err
	}
	return group, nil
}

func (r *ACLRepository) UpdateGroup(ctx context.Context, slug string, input UpdateACLGroupInput) (ACLGroup, error) {
	normalizedSlug, err := NormalizeACLGroupSlug(slug)
	if err != nil {
		return ACLGroup{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ACLGroup{}, fmt.Errorf("ACL group name is required")
	}
	description := strings.TrimSpace(input.Description)
	policy, err := NormalizeACLPolicy(input.Policy)
	if err != nil {
		return ACLGroup{}, err
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return ACLGroup{}, fmt.Errorf("encoding ACL group policy: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ACLGroup{}, fmt.Errorf("beginning ACL group update: %w", err)
	}
	defer tx.Rollback(ctx)

	group, err := loadACLGroupForUpdate(ctx, tx, normalizedSlug)
	if err != nil {
		return ACLGroup{}, err
	}
	if group.Protected {
		return ACLGroup{}, ErrProtectedACLGroup
	}

	var rawPolicy []byte
	err = tx.QueryRow(ctx, `
		UPDATE public.acl_groups
		SET name = $2,
		    description = $3,
		    policy = $4,
		    updated_at = now()
		WHERE slug = $1
		RETURNING id, slug, name, description, policy, built_in, protected, created_at, updated_at`,
		normalizedSlug, name, description, policyJSON).
		Scan(&group.ID, &group.Slug, &group.Name, &group.Description, &rawPolicy, &group.BuiltIn, &group.Protected, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		return ACLGroup{}, fmt.Errorf("updating ACL group: %w", err)
	}
	group.Policy = policy
	if err := bumpPolicyRevisionTx(ctx, tx); err != nil {
		return ACLGroup{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ACLGroup{}, fmt.Errorf("committing ACL group update: %w", err)
	}
	return group, nil
}

func (r *ACLRepository) DeleteGroup(ctx context.Context, slug string) error {
	normalizedSlug, err := NormalizeACLGroupSlug(slug)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("beginning ACL group delete: %w", err)
	}
	defer tx.Rollback(ctx)

	group, err := loadACLGroupForUpdate(ctx, tx, normalizedSlug)
	if err != nil {
		return err
	}
	if group.BuiltIn || group.Protected {
		return ErrProtectedACLGroup
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM public.acl_rules
		WHERE subject_type = 'group' AND subject_id = $1`, normalizedSlug); err != nil {
		return fmt.Errorf("deleting ACL group rules: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM public.acl_groups WHERE slug = $1`, normalizedSlug); err != nil {
		return fmt.Errorf("deleting ACL group: %w", err)
	}
	if err := bumpPolicyRevisionTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing ACL group delete: %w", err)
	}
	return nil
}

func (r *ACLRepository) ListGroupsByUserIDs(ctx context.Context, userIDs []int) (map[int][]ACLGroup, error) {
	out := make(map[int][]ACLGroup, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}

	rows, err := r.db.Query(ctx, `
		SELECT gm.user_id, g.id, g.slug, g.name, g.description, g.policy, g.built_in, g.protected, g.created_at, g.updated_at
		FROM public.acl_group_members gm
		JOIN public.acl_groups g ON g.id = gm.group_id
		WHERE gm.user_id = ANY($1::int[])
		ORDER BY gm.user_id ASC, g.id ASC`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("listing ACL groups by user: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var userID int
		var group ACLGroup
		var rawPolicy []byte
		if err := rows.Scan(&userID, &group.ID, &group.Slug, &group.Name, &group.Description, &rawPolicy, &group.BuiltIn, &group.Protected, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning ACL group membership: %w", err)
		}
		policy, err := decodeACLPolicy(rawPolicy)
		if err != nil {
			return nil, err
		}
		group.Policy = policy
		out[userID] = append(out[userID], group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ACL group memberships: %w", err)
	}
	return out, nil
}

func (r *ACLRepository) ListGroupMemberCounts(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT g.slug, count(gm.user_id)::int
		FROM public.acl_groups g
		LEFT JOIN public.acl_group_members gm ON gm.group_id = g.id
		GROUP BY g.slug`)
	if err != nil {
		return nil, fmt.Errorf("listing ACL group member counts: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var slug string
		var count int
		if err := rows.Scan(&slug, &count); err != nil {
			return nil, fmt.Errorf("scanning ACL group member count: %w", err)
		}
		out[slug] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ACL group member counts: %w", err)
	}
	return out, nil
}

func (r *ACLRepository) ListGroupMembers(ctx context.Context, slug string) ([]ACLGroupMember, error) {
	normalizedSlug, err := NormalizeACLGroupSlug(slug)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT u.id, u.username, u.email, u.role, u.enabled
		FROM public.acl_group_members gm
		JOIN public.acl_groups g ON g.id = gm.group_id
		JOIN public.users u ON u.id = gm.user_id
		WHERE g.slug = $1
		ORDER BY lower(u.username) ASC, u.id ASC`, normalizedSlug)
	if err != nil {
		return nil, fmt.Errorf("listing ACL group members: %w", err)
	}
	defer rows.Close()

	members := []ACLGroupMember{}
	for rows.Next() {
		var member ACLGroupMember
		if err := rows.Scan(&member.UserID, &member.Username, &member.Email, &member.Role, &member.Enabled); err != nil {
			return nil, fmt.Errorf("scanning ACL group member: %w", err)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ACL group members: %w", err)
	}
	return members, nil
}

func (r *ACLRepository) ReplaceUserGroups(ctx context.Context, userID int, groupSlugs []string) error {
	normalized, err := NormalizeACLGroupSlugs(groupSlugs)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("beginning ACL group update: %w", err)
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.users WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return fmt.Errorf("checking user for ACL group update: %w", err)
	}
	if !exists {
		return ErrNotFound
	}

	groupIDs := make([]int64, 0, len(normalized))
	if len(normalized) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT id, slug
			FROM public.acl_groups
			WHERE slug = ANY($1::text[])
			ORDER BY id ASC`, normalized)
		if err != nil {
			return fmt.Errorf("loading ACL groups: %w", err)
		}
		found := map[string]struct{}{}
		for rows.Next() {
			var id int64
			var slug string
			if err := rows.Scan(&id, &slug); err != nil {
				rows.Close()
				return fmt.Errorf("scanning ACL group id: %w", err)
			}
			groupIDs = append(groupIDs, id)
			found[slug] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterating ACL group ids: %w", err)
		}
		rows.Close()
		if len(found) != len(normalized) {
			return ErrUnknownACLGroup
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM public.acl_group_members WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("clearing ACL group memberships: %w", err)
	}
	for _, groupID := range groupIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.acl_group_members (group_id, user_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, groupID, userID); err != nil {
			return fmt.Errorf("inserting ACL group membership: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE public.users
		SET access_policy_revision = access_policy_revision + 1,
		    updated_at = now()
		WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("bumping user ACL revision: %w", err)
	}
	if err := bumpPolicyRevisionTx(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing ACL group update: %w", err)
	}
	return nil
}

func (r *ACLRepository) ReplaceGroupRules(ctx context.Context, slug string, inputs []ACLRuleInput) ([]ACLRule, error) {
	normalizedSlug, err := NormalizeACLGroupSlug(slug)
	if err != nil {
		return nil, err
	}
	normalizedInputs, err := NormalizeACLRuleInputs(inputs)
	if err != nil {
		return nil, err
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("beginning ACL rule update: %w", err)
	}
	defer tx.Rollback(ctx)

	group, err := loadACLGroupForUpdate(ctx, tx, normalizedSlug)
	if err != nil {
		return nil, err
	}
	if group.Protected {
		return nil, ErrProtectedACLGroup
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM public.acl_rules
		WHERE subject_type = 'group' AND subject_id = $1`, normalizedSlug); err != nil {
		return nil, fmt.Errorf("clearing ACL group rules: %w", err)
	}
	for _, rule := range normalizedInputs {
		conditions, err := json.Marshal(rule.Conditions)
		if err != nil {
			return nil, fmt.Errorf("encoding ACL rule conditions: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.acl_rules
				(subject_type, subject_id, action, resource_type, resource_id, effect, conditions, priority, name, description)
			VALUES
				('group', $1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			normalizedSlug, rule.Action, rule.ResourceType, rule.ResourceID, rule.Effect, conditions, rule.Priority, rule.Name, rule.Description); err != nil {
			return nil, fmt.Errorf("inserting ACL group rule: %w", err)
		}
	}
	if err := bumpPolicyRevisionTx(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing ACL rule update: %w", err)
	}
	return r.listRulesForGroup(ctx, normalizedSlug)
}

func (r *ACLRepository) listRulesForGroup(ctx context.Context, slug string) ([]ACLRule, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, subject_type, subject_id, action, resource_type, resource_id, effect, conditions, priority, name, description
		FROM public.acl_rules
		WHERE subject_type = 'group' AND subject_id = $1
		ORDER BY priority DESC, id ASC`, slug)
	if err != nil {
		return nil, fmt.Errorf("listing ACL group rules: %w", err)
	}
	defer rows.Close()

	rules := []ACLRule{}
	for rows.Next() {
		var row aclRuleRow
		if err := rows.Scan(&row.ID, &row.SubjectType, &row.SubjectID, &row.Action, &row.ResourceType, &row.ResourceID, &row.Effect, &row.Conditions, &row.Priority, &row.Name, &row.Description); err != nil {
			return nil, fmt.Errorf("scanning ACL group rule: %w", err)
		}
		rule, err := row.toRule()
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ACL group rules: %w", err)
	}
	return rules, nil
}

type aclTx interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func loadACLGroupForUpdate(ctx context.Context, tx aclTx, slug string) (ACLGroup, error) {
	var group ACLGroup
	var rawPolicy []byte
	err := tx.QueryRow(ctx, `
		SELECT id, slug, name, description, policy, built_in, protected, created_at, updated_at
		FROM public.acl_groups
		WHERE slug = $1
		FOR UPDATE`, slug).
		Scan(&group.ID, &group.Slug, &group.Name, &group.Description, &rawPolicy, &group.BuiltIn, &group.Protected, &group.CreatedAt, &group.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ACLGroup{}, ErrNotFound
		}
		return ACLGroup{}, fmt.Errorf("loading ACL group: %w", err)
	}
	policy, err := decodeACLPolicy(rawPolicy)
	if err != nil {
		return ACLGroup{}, err
	}
	group.Policy = policy
	return group, nil
}

func (r *ACLRepository) bumpPolicyRevision(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO public.acl_policy_revisions (id, revision, updated_at)
		VALUES (true, 1, now())
		ON CONFLICT (id) DO UPDATE
		SET revision = public.acl_policy_revisions.revision + 1,
		    updated_at = now()`)
	if err != nil {
		return fmt.Errorf("bumping ACL policy revision: %w", err)
	}
	return nil
}

func bumpPolicyRevisionTx(ctx context.Context, tx aclTx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.acl_policy_revisions (id, revision, updated_at)
		VALUES (true, 1, now())
		ON CONFLICT (id) DO UPDATE
		SET revision = public.acl_policy_revisions.revision + 1,
		    updated_at = now()`); err != nil {
		return fmt.Errorf("bumping ACL policy revision: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
