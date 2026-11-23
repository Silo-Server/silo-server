package nodepool

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Names of the configuration fields a node can declare from its environment.
// They are the field names of the admin API's node update body, and the
// values stream_nodes.env_managed holds.
const (
	FieldName                             = "name"
	FieldURL                              = "url"
	FieldPublicURL                        = "public_url"
	FieldGroup                            = "group"
	FieldMaxJobs                          = "max_jobs"
	FieldMaxBandwidthKbps                 = "max_bandwidth_kbps"
	FieldSourceAccess                     = "source_access"
	FieldHWAccelOverride                  = "hw_accel_override"
	FieldHWDeviceOverride                 = "hw_device_override"
	FieldDownloadArtifactDirOverride      = "download_artifact_dir_override"
	FieldDownloadArtifactMaxBytesOverride = "download_artifact_max_bytes_override"
)

// declarableFields is what each node type may declare, in the order
// env_managed stores them. A proxy's url and name are its identity: they are
// set when the proxy registers itself and never declared afterwards.
var declarableFields = map[string][]string{
	NodeTypeProxy: {FieldPublicURL, FieldGroup, FieldMaxJobs, FieldMaxBandwidthKbps},
	NodeTypeTranscode: {FieldName, FieldURL, FieldGroup, FieldMaxJobs, FieldSourceAccess,
		FieldHWAccelOverride, FieldHWDeviceOverride, FieldDownloadArtifactDirOverride, FieldDownloadArtifactMaxBytesOverride},
}

// DeclarableFields lists the fields a node of nodeType can declare.
func DeclarableFields(nodeType string) []string {
	return slices.Clone(declarableFields[nodeType])
}

// SetFields names the configuration fields an update sets, in a fixed order.
// Enabled is not configuration a node declares, so it is never named.
func (i UpdateNodeInput) SetFields() []string {
	var fields []string
	add := func(set bool, name string) {
		if set {
			fields = append(fields, name)
		}
	}
	add(i.Name != nil, FieldName)
	add(i.URL != nil, FieldURL)
	add(i.PublicURL != nil, FieldPublicURL)
	add(i.Group != nil, FieldGroup)
	add(i.MaxJobs != nil, FieldMaxJobs)
	add(i.MaxBandwidthKbps != nil, FieldMaxBandwidthKbps)
	add(i.SourceAccess != nil, FieldSourceAccess)
	add(i.HWAccelOverride != nil, FieldHWAccelOverride)
	add(i.HWDeviceOverride != nil, FieldHWDeviceOverride)
	add(i.DownloadArtifactDirOverride != nil, FieldDownloadArtifactDirOverride)
	add(i.DownloadArtifactMaxBytesOverride != nil, FieldDownloadArtifactMaxBytesOverride)
	return fields
}

// only returns the part of i that sets field, so one field can be validated
// on its own and a refusal can name it.
func (i UpdateNodeInput) only(field string) UpdateNodeInput {
	var o UpdateNodeInput
	switch field {
	case FieldName:
		o.Name = i.Name
	case FieldURL:
		o.URL = i.URL
	case FieldPublicURL:
		o.PublicURL = i.PublicURL
	case FieldGroup:
		o.Group = i.Group
	case FieldMaxJobs:
		o.MaxJobs = i.MaxJobs
	case FieldMaxBandwidthKbps:
		o.MaxBandwidthKbps = i.MaxBandwidthKbps
	case FieldSourceAccess:
		o.SourceAccess = i.SourceAccess
	case FieldHWAccelOverride:
		o.HWAccelOverride = i.HWAccelOverride
	case FieldHWDeviceOverride:
		o.HWDeviceOverride = i.HWDeviceOverride
	case FieldDownloadArtifactDirOverride:
		o.DownloadArtifactDirOverride = i.DownloadArtifactDirOverride
	case FieldDownloadArtifactMaxBytesOverride:
		o.DownloadArtifactMaxBytesOverride = i.DownloadArtifactMaxBytesOverride
	}
	return o
}

// fieldValue is a stored row's value of one declarable field, compared before
// and after an administrator update to tell whether the update changed it.
func fieldValue(n *Node, field string) any {
	switch field {
	case FieldName:
		return n.Name
	case FieldURL:
		return n.URL
	case FieldPublicURL:
		return n.PublicURL
	case FieldGroup:
		return n.Group
	case FieldMaxJobs:
		return n.MaxJobs
	case FieldMaxBandwidthKbps:
		return n.MaxBandwidthKbps
	case FieldSourceAccess:
		return n.SourceAccess
	case FieldHWAccelOverride:
		return n.HWAccelOverride
	case FieldHWDeviceOverride:
		return n.HWDeviceOverride
	case FieldDownloadArtifactDirOverride:
		return n.DownloadArtifactDirOverride
	case FieldDownloadArtifactMaxBytesOverride:
		return n.DownloadArtifactMaxBytesOverride
	}
	return nil
}

// ErrEnvManagedField marks an administrator update refused because it would
// change a field the node declares from its environment.
var ErrEnvManagedField = errors.New("field is set by the node's environment")

// EnvManagedFieldsError lists the declared fields an administrator update
// would have changed. It matches ErrEnvManagedField.
type EnvManagedFieldsError struct {
	Fields []string
}

func (e *EnvManagedFieldsError) Error() string {
	return fmt.Sprintf("%s: %s", ErrEnvManagedField, strings.Join(e.Fields, ", "))
}

func (e *EnvManagedFieldsError) Is(target error) bool { return target == ErrEnvManagedField }

// changedEnvManagedFields names the fields the row declares whose stored value
// differs between before and after. Comparing the stored rows, rather than the
// request against the row, lets an update that restates a declared value in
// another spelling the store normalizes ("" against null, 0 against an
// unlimited cap) through, which is what a full-form save sends.
func changedEnvManagedFields(before, after *Node) []string {
	var changed []string
	for _, field := range before.EnvManaged {
		if !reflect.DeepEqual(fieldValue(before, field), fieldValue(after, field)) {
			changed = append(changed, field)
		}
	}
	return changed
}

// DeclarationError refuses one declared field: a value the store would not
// accept, or a field the node's type cannot declare. It matches
// ErrInvalidNodeInput.
type DeclarationError struct {
	Field  string
	Detail string
}

func (e *DeclarationError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrInvalidNodeInput, e.Field, e.Detail)
}

func (e *DeclarationError) Is(target error) bool { return target == ErrInvalidNodeInput }

// validateDeclaration checks a declaration against the row it applies to with
// the rules of an administrator update, one field at a time so a refusal
// names its field. A declaration may not name a field its node's type cannot
// declare, and may not use the clear sentinels an update uses for "restore the
// default": an undeclared field is how a node leaves a value to the
// administrator.
func validateDeclaration(node *Node, input UpdateNodeInput) error {
	if input.Enabled != nil {
		return &DeclarationError{Field: "enabled", Detail: "a node cannot declare whether it is enabled"}
	}
	allowed := declarableFields[node.Type]
	for _, field := range input.SetFields() {
		if !slices.Contains(allowed, field) {
			return &DeclarationError{Field: field, Detail: "a " + node.Type + " node cannot declare this field"}
		}
		one := input.only(field)
		err := one.Validate()
		if err == nil {
			err = one.ValidateFor(node)
		}
		if err != nil {
			return &DeclarationError{Field: field, Detail: strings.TrimPrefix(err.Error(), ErrInvalidNodeInput.Error()+": ")}
		}
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		return &DeclarationError{Field: FieldName, Detail: "must not be empty"}
	}
	if input.URL != nil && !absoluteHTTPURL(*input.URL) {
		return &DeclarationError{Field: FieldURL, Detail: detailAbsoluteURL}
	}
	if input.PublicURL != nil && !absoluteHTTPURL(*input.PublicURL) {
		return &DeclarationError{Field: FieldPublicURL, Detail: detailAbsoluteURL}
	}
	for field, value := range map[string]*int{FieldMaxJobs: input.MaxJobs, FieldMaxBandwidthKbps: input.MaxBandwidthKbps} {
		if value != nil && *value < 0 {
			return &DeclarationError{Field: field, Detail: "must be 0 (unlimited) or more"}
		}
	}
	if input.DownloadArtifactMaxBytesOverride != nil && *input.DownloadArtifactMaxBytesOverride < 0 {
		return &DeclarationError{Field: FieldDownloadArtifactMaxBytesOverride, Detail: "must be 0 (no budget) or more"}
	}
	for field, value := range map[string]*string{FieldHWAccelOverride: input.HWAccelOverride, FieldHWDeviceOverride: input.HWDeviceOverride,
		FieldDownloadArtifactDirOverride: input.DownloadArtifactDirOverride, FieldSourceAccess: input.SourceAccess} {
		if value != nil && strings.TrimSpace(*value) == "" {
			return &DeclarationError{Field: field, Detail: "must not be empty"}
		}
	}
	return nil
}

// ValidateDeclaration checks a declaration for a node of nodeType with the
// rules the core applies, so a node can refuse a bad value before it sends it.
func ValidateDeclaration(nodeType string, input UpdateNodeInput) error {
	return validateDeclaration(&Node{Type: nodeType}, input)
}

// detailAbsoluteURL refuses a declared or self-registered URL.
const detailAbsoluteURL = "must be an absolute http or https URL"

// absoluteHTTPURL reports whether value is an absolute http or https URL.
func absoluteHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// Declare applies the configuration node id declares from its environment:
// the declared fields are validated as an administrator update is and
// written, and env_managed becomes exactly the declared field names, so a
// field whose variable was removed is the administrator's again. An empty
// declaration unlocks every field and changes nothing else. Previous is the
// row before the write, for the caller's post-commit work.
func (s *AdminConfigurationStore) Declare(ctx context.Context, id int, input UpdateNodeInput) (node, previous *Node, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if input.URL != nil {
		// Before the row lock, as Update takes it.
		if err = lockTranscodeURLs(ctx, tx); err != nil {
			return nil, nil, err
		}
	}
	node, previous, err = declareInTx(ctx, tx, id, input)
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return node, previous, nil
}

// declareInTx is Declare inside the caller's transaction, which holds the
// transcode URL lock when the declaration names a URL.
func declareInTx(ctx context.Context, tx pgx.Tx, id int, input UpdateNodeInput) (node, previous *Node, err error) {
	revision, err := readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	repo := NewTransactionalRepository(tx)
	previous, err = repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	previous.AdminRevision = revision
	if err = validateDeclaration(previous, input); err != nil {
		return nil, nil, err
	}
	if input.URL != nil && previous.Type == NodeTypeTranscode && CanonicalNodeURL(*input.URL) != CanonicalNodeURL(previous.URL) {
		if err = checkDeclaredURLFree(ctx, tx, id, *input.URL); err != nil {
			return nil, nil, err
		}
	}
	if node, err = repo.Update(ctx, id, input); err != nil {
		return nil, nil, err
	}
	fields := input.SetFields()
	if fields == nil {
		fields = []string{}
	}
	// A node sends its declaration every minute, so an unchanged one leaves
	// env_managed alone rather than writing the same array again.
	if !slices.Equal(previous.EnvManaged, fields) {
		if _, err = tx.Exec(ctx, `UPDATE stream_nodes SET env_managed = $2 WHERE id = $1`, id, fields); err != nil {
			return nil, nil, fmt.Errorf("record declared node fields: %w", err)
		}
	}
	node.EnvManaged = fields
	if node.AdminRevision, err = readConfigurationRevision(ctx, tx, id); err != nil {
		return nil, nil, err
	}
	return node, previous, nil
}

// checkDeclaredURLFree refuses a URL a node declares when any other row,
// proxy or transcode, has its canonical form. The credential registry finds
// nodes by that address and prefers a transcode row there, so a node's key
// must not be able to take a proxy's address under another spelling and put
// the proxy out of use. An administrator's update is held only to the
// transcode rows (checkTranscodeURLFree).
func checkDeclaredURLFree(ctx context.Context, tx pgx.Tx, exceptID int, nodeURL string) error {
	rows, err := tx.Query(ctx, `SELECT url FROM stream_nodes WHERE id <> $1`, exceptID)
	if err != nil {
		return err
	}
	urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	canonical := CanonicalNodeURL(nodeURL)
	for _, other := range urls {
		if CanonicalNodeURL(other) == canonical {
			return ErrTranscodeNodeURLTaken
		}
	}
	return nil
}

// ErrProxyNotRegistered reports a proxy that matches no row and was not asked
// to register itself.
var ErrProxyNotRegistered = errors.New("no stream node row matches this proxy")

// ErrNodeIdentityConflict refuses a proxy's self-registration when its
// identity matches a row that is not a proxy, or several rows by name.
var ErrNodeIdentityConflict = errors.New("this node's URL or name matches a row it cannot register as")

// proxyRegistrationLock serializes proxy self-registration, so two proxies
// started at once with the same name cannot both find no row and create two.
const proxyRegistrationLock = "nodepool.stream_nodes.proxy_registration"

// ProxyIdentity is how a proxy names itself when it declares.
type ProxyIdentity struct {
	// URL is the proxy's own address.
	URL string
	// Name finds the row when no row has URL: the proxy's name variable, or
	// for an unnamed proxy that does not register itself the configuration
	// watcher's fallback name, so the declaration lands on the row the
	// watcher binds the proxy to. Empty skips the lookup by name.
	Name string
	// NewRowName names a row the proxy creates. Empty uses the URL's host.
	NewRowName string
}

// DeclareProxy applies a proxy's declaration to its row. The row is found as
// the proxy's configuration watcher finds it: by URL with trailing slashes
// ignored, then by name when exactly one row has it. A proxy with no row
// gets ErrProxyNotRegistered unless register is set; with it, the proxy
// creates one, enabled, at its URL. Registration is opt-in because a proxy
// whose URL and name match nothing may still have a row, registered at an
// address clients reach it by, and a second row at nodeURL would hand clients
// an address they cannot reach. The row's url and name are its identity and
// are never changed here. created reports a new row.
func (s *AdminConfigurationStore) DeclareProxy(ctx context.Context, identity ProxyIdentity, input UpdateNodeInput, register bool) (node *Node, created bool, err error) {
	nodeURL, nodeName := strings.TrimSpace(identity.URL), strings.TrimSpace(identity.Name)
	if !absoluteHTTPURL(nodeURL) {
		return nil, false, &DeclarationError{Field: FieldURL, Detail: detailAbsoluteURL}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if register {
		// A row this creates takes an address, so it waits its turn with the
		// writes that give a transcode node one. Taken first, as they take it.
		if err = lockTranscodeURLs(ctx, tx); err != nil {
			return nil, false, err
		}
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, proxyRegistrationLock); err != nil {
		return nil, false, err
	}
	id, err := findProxyRow(ctx, tx, nodeURL, nodeName)
	if err != nil {
		return nil, false, err
	}
	if id == 0 {
		if !register {
			return nil, false, ErrProxyNotRegistered
		}
		// No row has this URL as written, but one may under another spelling.
		if errors.Is(checkDeclaredURLFree(ctx, tx, 0, nodeURL), ErrTranscodeNodeURLTaken) {
			return nil, false, fmt.Errorf("%w: another node already has this URL under another spelling", ErrNodeIdentityConflict)
		}
		if id, err = createProxyRow(ctx, tx, nodeURL, strings.TrimSpace(identity.NewRowName)); err != nil {
			return nil, false, err
		}
		created = true
	}
	node, _, err = declareInTx(ctx, tx, id, input)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return node, created, nil
}

// createProxyRow inserts an enabled proxy at nodeURL named nodeName, or the
// URL's host when nodeName is empty, and returns its id.
func createProxyRow(ctx context.Context, tx pgx.Tx, nodeURL, nodeName string) (int, error) {
	name := nodeName
	if name == "" {
		if parsed, err := url.Parse(nodeURL); err == nil {
			name = parsed.Host
		}
	}
	row, err := NewTransactionalRepository(tx).Create(ctx, CreateNodeInput{Name: name, Type: NodeTypeProxy, URL: nodeURL})
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

// findProxyRow returns the id of the proxy row nodeURL or nodeName names, or 0
// when none does.
func findProxyRow(ctx context.Context, tx pgx.Tx, nodeURL, nodeName string) (int, error) {
	type match struct {
		ID   int
		Type string
	}
	query := func(sql string, arg any) ([]match, error) {
		rows, err := tx.Query(ctx, sql, arg)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByPos[match])
	}
	matches, err := query(`SELECT id, type FROM stream_nodes WHERE rtrim(url, '/') = rtrim($1, '/') ORDER BY id LIMIT 1`, nodeURL)
	if err != nil {
		return 0, err
	}
	if len(matches) == 0 && nodeName != "" {
		if matches, err = query(`SELECT id, type FROM stream_nodes WHERE name = $1 ORDER BY id LIMIT 2`, nodeName); err != nil {
			return 0, err
		}
		if len(matches) > 1 {
			return 0, fmt.Errorf("%w: several rows are named %q", ErrNodeIdentityConflict, nodeName)
		}
	}
	if len(matches) == 0 {
		return 0, nil
	}
	if matches[0].Type != NodeTypeProxy {
		return 0, fmt.Errorf("%w: row %d is a %s node", ErrNodeIdentityConflict, matches[0].ID, matches[0].Type)
	}
	return matches[0].ID, nil
}
