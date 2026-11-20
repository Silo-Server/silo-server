package nodepool

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// ProvisionError is a declared node the API server will not apply. It stops
// startup: serving with a declaration half applied would leave a node the
// operator listed without a usable key.
type ProvisionError struct {
	Name   string
	ID     int
	Detail string
}

func (e *ProvisionError) Error() string {
	return fmt.Sprintf("nodes.%s (node %d): %s", e.Name, e.ID, e.Detail)
}

// ProvisionResult lists the node ids a provisioning pass changed.
type ProvisionResult struct {
	// Created rows, with their key.
	Created []int
	// Rotated keys of rows the deployment already managed.
	Rotated []int
	// Adopted rows that already held the declared key.
	Adopted []int
	// Released rows the declaration no longer lists: their key and
	// configuration stay, and the admin API manages them again.
	Released []int
}

// Changed reports a pass that wrote anything.
func (r ProvisionResult) Changed() bool {
	return len(r.Created)+len(r.Rotated)+len(r.Adopted)+len(r.Released) > 0
}

// provisionRow is what provisioning reads of an existing row.
type provisionRow struct {
	ID         int
	Type       string
	Ciphertext *string
	Managed    bool
}

// ProvisionTranscodeNodes applies the deployment's transcode node declaration
// in one transaction:
//
//   - A declared id with no row gets one: transcode, enabled, with the
//     declared name and URL and the key. Only an id the identity sequence has
//     never handed out can be created, so no id ever names two machines.
//   - A row the deployment already manages keeps its ciphertext when the
//     declared key is the one stored, and takes the declared key otherwise.
//   - A transcode row the deployment does not manage is adopted only when it
//     already holds the declared key; any other row with a declared id stops
//     startup, so a declaration never takes over another machine's row.
//   - A managed row the declaration no longer lists is released.
//
// Name and URL are used only to create a row; afterwards they belong to the
// node's own declaration and the administrator. The caller publishes
// node_pool_changed when the result changed anything.
//
// It takes the transcode URL lock, then an EXCLUSIVE lock on stream_nodes,
// which waits for every transaction holding a row lock or a write and holds
// off new ones (and the nextval an insert draws) until it commits. A weaker
// mode would admit an admin edit's SELECT ... FOR UPDATE, and the two would
// deadlock when both then write that row. Plain reads go on. Two API servers
// starting together run one after the other.
func ProvisionTranscodeNodes(ctx context.Context, pool *pgxpool.Pool, cipher *secret.Cipher, nodes []ProvisionedNode) (ProvisionResult, error) {
	if cipher == nil {
		return ProvisionResult{}, errors.New("provision transcode nodes: no data cipher")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ProvisionResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err = lockTranscodeURLs(ctx, tx); err != nil {
		return ProvisionResult{}, err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE stream_nodes IN EXCLUSIVE MODE`); err != nil {
		return ProvisionResult{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id, type, node_key_ciphertext, deployment_managed FROM stream_nodes`)
	if err != nil {
		return ProvisionResult{}, err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowToStructByPos[provisionRow])
	if err != nil {
		return ProvisionResult{}, err
	}
	sequence, watermark, err := identityWatermark(ctx, tx)
	if err != nil {
		return ProvisionResult{}, err
	}
	// Decide everything before writing, so a refusal leaves the sequence and
	// every row as they were.
	plan, err := planProvision(ctx, tx, cipher, nodes, existing, watermark)
	if err != nil {
		return ProvisionResult{}, err
	}
	if err = plan.apply(ctx, tx, sequence); err != nil {
		return ProvisionResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProvisionResult{}, fmt.Errorf("commit node provisioning: %w", err)
	}
	return plan.result, nil
}

// provisionPlan is what a provisioning pass writes: rows to create (in id
// order), ciphertexts to replace, rows to adopt and rows to release.
type provisionPlan struct {
	creates []provisionCreate
	rotates map[int]string
	result  ProvisionResult
}

type provisionCreate struct {
	node       ProvisionedNode
	ciphertext string
}

func planProvision(ctx context.Context, tx pgx.Tx, cipher *secret.Cipher, nodes []ProvisionedNode, existing []provisionRow, watermark int) (*provisionPlan, error) {
	plan := &provisionPlan{rotates: map[int]string{}}
	byID := make(map[int]provisionRow, len(existing))
	for _, row := range existing {
		byID[row.ID] = row
	}
	declared := make(map[int]bool, len(nodes))
	for _, node := range nodes {
		declared[node.ID] = true
		row, found := byID[node.ID]
		var err error
		if found {
			err = plan.existing(cipher, node, row)
		} else {
			err = plan.create(ctx, tx, cipher, node, watermark)
		}
		if err != nil {
			return nil, err
		}
	}
	for _, row := range existing {
		if row.Managed && !declared[row.ID] {
			plan.result.Released = append(plan.result.Released, row.ID)
		}
	}
	slices.Sort(plan.result.Released)
	return plan, nil
}

// create plans a row for a declared id that has none.
func (p *provisionPlan) create(ctx context.Context, tx pgx.Tx, cipher *secret.Cipher, node ProvisionedNode, watermark int) error {
	if node.ID <= watermark {
		return &ProvisionError{Name: node.Name, ID: node.ID,
			Detail: fmt.Sprintf("this id may have named a node before; a new node takes an id above %d", watermark)}
	}
	if err := checkDeclaredURLFree(ctx, tx, 0, node.URL); errors.Is(err, ErrTranscodeNodeURLTaken) {
		return &ProvisionError{Name: node.Name, ID: node.ID, Detail: "another node already has this URL"}
	} else if err != nil {
		return err
	}
	ciphertext, err := EncryptNodeKey(cipher, node.ID, node.Key)
	if err != nil {
		return err
	}
	p.creates = append(p.creates, provisionCreate{node: node, ciphertext: ciphertext})
	return nil
}

// existing plans what a declared id's existing row needs.
func (p *provisionPlan) existing(cipher *secret.Cipher, node ProvisionedNode, row provisionRow) error {
	refuse := func(detail string) error { return &ProvisionError{Name: node.Name, ID: node.ID, Detail: detail} }
	if row.Type != NodeTypeTranscode {
		return refuse("this id is a proxy node; choose another id")
	}
	keyless := row.Ciphertext == nil || *row.Ciphertext == ""
	stored := ""
	if !keyless {
		stored, _ = DecryptNodeKey(cipher, node.ID, *row.Ciphertext)
	}
	same := stored != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(node.Key)) == 1
	switch {
	case row.Managed && same:
		return nil
	case row.Managed:
		// Rotated in the secret store, or stored under another SECRET_KEY.
		ciphertext, err := EncryptNodeKey(cipher, node.ID, node.Key)
		if err != nil {
			return err
		}
		p.rotates[node.ID] = ciphertext
		p.result.Rotated = append(p.result.Rotated, node.ID)
		return nil
	case same:
		p.result.Adopted = append(p.result.Adopted, node.ID)
		return nil
	case keyless:
		return refuse("this node exists and has no key; choose another id, or issue it a key in the admin settings and reference that key")
	default:
		return refuse("this node exists with a different key, issued in the admin settings; choose another id, or issue it a key there and reference that key")
	}
}

// apply writes the plan. The identity sequence moves past every created id
// before they exist; the table lock holds off every nextval until commit.
// ALTER SEQUENCE ... RESTART rolls back with the transaction, where setval
// does not: a pass that fails after it leaves its ids free for the next try.
func (p *provisionPlan) apply(ctx context.Context, tx pgx.Tx, sequence string) error {
	if len(p.creates) > 0 {
		// The name comes from PostgreSQL, already quoted where it needs to
		// be; the id is at most MaxProvisionedNodeID, so the next one fits.
		next := p.creates[len(p.creates)-1].node.ID + 1
		if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER SEQUENCE %s RESTART WITH %d`, sequence, next)); err != nil {
			return fmt.Errorf("advance node id sequence: %w", err)
		}
	}
	for _, c := range p.creates {
		if _, err := tx.Exec(ctx,
			`INSERT INTO stream_nodes (id, name, type, url, node_key_ciphertext, deployment_managed)
			 VALUES ($1, $2, $3, $4, $5, true)`,
			c.node.ID, c.node.Name, NodeTypeTranscode, c.node.URL, c.ciphertext); err != nil {
			return fmt.Errorf("create node %d: %w", c.node.ID, err)
		}
		p.result.Created = append(p.result.Created, c.node.ID)
	}
	for id, ciphertext := range p.rotates {
		if _, err := tx.Exec(ctx, `UPDATE stream_nodes SET node_key_ciphertext = $2 WHERE id = $1`, id, ciphertext); err != nil {
			return fmt.Errorf("rotate node %d key: %w", id, err)
		}
	}
	if err := setDeploymentManaged(ctx, tx, p.result.Adopted, true); err != nil {
		return err
	}
	return setDeploymentManaged(ctx, tx, p.result.Released, false)
}

// setDeploymentManaged records whether the deployment owns the rows ids.
func setDeploymentManaged(ctx context.Context, tx pgx.Tx, ids []int, managed bool) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE stream_nodes SET deployment_managed = $2 WHERE id = ANY($1)`, ids, managed); err != nil {
		return fmt.Errorf("record node ownership: %w", err)
	}
	return nil
}

// identityWatermark returns stream_nodes' identity sequence and the highest id
// it has handed out (0 when none), never below the highest id in the table.
// Ids at or below it may have named a node that is gone now.
func identityWatermark(ctx context.Context, tx pgx.Tx) (string, int, error) {
	var sequence string
	if err := tx.QueryRow(ctx, `SELECT pg_get_serial_sequence('public.stream_nodes', 'id')`).Scan(&sequence); err != nil {
		return "", 0, fmt.Errorf("find node id sequence: %w", err)
	}
	var (
		last   int64
		called bool
		maxID  int
	)
	// The name comes from PostgreSQL, already quoted where it needs to be.
	if err := tx.QueryRow(ctx, `SELECT last_value, is_called FROM `+sequence).Scan(&last, &called); err != nil {
		return "", 0, fmt.Errorf("read node id sequence: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM stream_nodes`).Scan(&maxID); err != nil {
		return "", 0, err
	}
	watermark := int(last)
	if !called {
		watermark--
	}
	return sequence, max(watermark, maxID), nil
}
