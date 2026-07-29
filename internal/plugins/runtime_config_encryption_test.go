package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/secret"
)

type runtimeConfigBackfillRows struct {
	id             int64
	installationID int
	key            string
	valueJSON      []byte
	yielded        bool
}

func (r *runtimeConfigBackfillRows) Next() bool {
	if r.yielded {
		return false
	}
	r.yielded = true
	return true
}

func (r *runtimeConfigBackfillRows) Scan(dest ...any) error {
	if len(dest) != 4 {
		return fmt.Errorf("scan destinations = %d, want 4", len(dest))
	}
	*dest[0].(*int64) = r.id
	*dest[1].(*int) = r.installationID
	*dest[2].(*string) = r.key
	*dest[3].(*[]byte) = append([]byte(nil), r.valueJSON...)
	return nil
}

func (r *runtimeConfigBackfillRows) Close()                                       {}
func (r *runtimeConfigBackfillRows) Err() error                                   { return nil }
func (r *runtimeConfigBackfillRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *runtimeConfigBackfillRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *runtimeConfigBackfillRows) Values() ([]any, error)                       { return nil, nil }
func (r *runtimeConfigBackfillRows) RawValues() [][]byte                          { return nil }
func (r *runtimeConfigBackfillRows) Conn() *pgx.Conn                              { return nil }

type runtimeConfigBackfillExec struct {
	row        runtimeConfigBackfillRows
	updateTag  pgconn.CommandTag
	updateSQL  string
	updateArgs []any
}

func (e *runtimeConfigBackfillExec) Query(
	_ context.Context,
	_ string,
	_ ...any,
) (pgx.Rows, error) {
	row := e.row
	return &row, nil
}

func (e *runtimeConfigBackfillExec) Exec(
	_ context.Context,
	sql string,
	args ...any,
) (pgconn.CommandTag, error) {
	e.updateSQL = sql
	e.updateArgs = append([]any(nil), args...)
	return e.updateTag, nil
}

func TestRuntimeConfigEncryptionRoundTrip(t *testing.T) {
	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	value := map[string]any{"api_key": "clawrouter-e2e-secret", "enabled": true}

	encoded, err := encodeRuntimeConfigValue(cipher, 42, "account", value)
	if err != nil {
		t.Fatalf("encodeRuntimeConfigValue: %v", err)
	}
	if bytes.Contains(encoded, []byte("clawrouter-e2e-secret")) {
		t.Fatalf("encoded config contains plaintext secret: %s", encoded)
	}
	decoded, err := decodeRuntimeConfigValue(cipher, 42, "account", encoded)
	if err != nil {
		t.Fatalf("decodeRuntimeConfigValue: %v", err)
	}
	if decoded["api_key"] != "clawrouter-e2e-secret" || decoded["enabled"] != true {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestRuntimeConfigEncryptionBindsInstallationAndKey(t *testing.T) {
	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	encoded, err := encodeRuntimeConfigValue(cipher, 42, "account", map[string]any{
		"api_key": "clawrouter-e2e-secret",
	})
	if err != nil {
		t.Fatalf("encodeRuntimeConfigValue: %v", err)
	}
	if _, err := decodeRuntimeConfigValue(cipher, 43, "account", encoded); err == nil {
		t.Fatal("decode with a different installation id succeeded")
	}
	if _, err := decodeRuntimeConfigValue(cipher, 42, "other", encoded); err == nil {
		t.Fatal("decode with a different config key succeeded")
	}
}

func TestRuntimeConfigLegacyPlaintextRemainsReadable(t *testing.T) {
	decoded, err := decodeRuntimeConfigValue(nil, 1, "account", []byte(`{"api_key":"clawrouter-e2e-secret"}`))
	if err != nil {
		t.Fatalf("decodeRuntimeConfigValue: %v", err)
	}
	if decoded["api_key"] != "clawrouter-e2e-secret" {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestRuntimeConfigUnrelatedEditPreservesLargeInteger(t *testing.T) {
	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	const largeInteger = "9007199254740993"
	encoded, err := encodeRuntimeConfigJSON(
		cipher,
		42,
		"account",
		[]byte(`{"cursor":`+largeInteger+`,"display_name":"old"}`),
	)
	if err != nil {
		t.Fatalf("encodeRuntimeConfigJSON: %v", err)
	}

	decoded, err := decodeRuntimeConfigValue(cipher, 42, "account", encoded)
	if err != nil {
		t.Fatalf("decodeRuntimeConfigValue: %v", err)
	}
	cursor, ok := decoded["cursor"].(json.Number)
	if !ok || cursor.String() != largeInteger {
		t.Fatalf("decoded cursor = %#v, want json.Number(%s)", decoded["cursor"], largeInteger)
	}
	decoded["display_name"] = "new"

	reencoded, err := encodeRuntimeConfigValue(cipher, 42, "account", decoded)
	if err != nil {
		t.Fatalf("encodeRuntimeConfigValue: %v", err)
	}
	var envelope map[string]string
	if err := json.Unmarshal(reencoded, &envelope); err != nil {
		t.Fatalf("unmarshal encrypted envelope: %v", err)
	}
	plaintext, err := cipher.Decrypt(
		envelope[encryptedRuntimeConfigField],
		runtimeConfigAAD(42, "account"),
	)
	if err != nil {
		t.Fatalf("decrypt edited value: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(plaintext), &raw); err != nil {
		t.Fatalf("unmarshal edited plaintext: %v", err)
	}
	if string(raw["cursor"]) != largeInteger {
		t.Fatalf("edited cursor = %s, want %s", raw["cursor"], largeInteger)
	}
	if string(raw["display_name"]) != `"new"` {
		t.Fatalf("edited display_name = %s, want new", raw["display_name"])
	}
}

func TestRuntimeConfigEncryptionBackfillPreservesLargeInteger(t *testing.T) {
	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	legacy := []byte(`{"cursor":9007199254740993}`)
	db := &runtimeConfigBackfillExec{
		row: runtimeConfigBackfillRows{
			id:             9,
			installationID: 42,
			key:            "account",
			valueJSON:      legacy,
		},
		updateTag: pgconn.NewCommandTag("UPDATE 1"),
	}

	updated, err := backfillEncryptedConfigs(context.Background(), db, cipher)
	if err != nil {
		t.Fatalf("backfillEncryptedConfigs: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	encoded := db.updateArgs[1].([]byte)
	var envelope map[string]string
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("unmarshal encrypted envelope: %v", err)
	}
	plaintext, err := cipher.Decrypt(
		envelope[encryptedRuntimeConfigField],
		runtimeConfigAAD(42, "account"),
	)
	if err != nil {
		t.Fatalf("decrypt backfilled value: %v", err)
	}
	if plaintext != string(legacy) {
		t.Fatalf("backfilled plaintext = %s, want %s", plaintext, legacy)
	}
}

func TestRuntimeConfigEncryptionBackfillSkipsConcurrentWrite(t *testing.T) {
	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	legacy := []byte(`{"region":"original"}`)
	db := &runtimeConfigBackfillExec{
		row: runtimeConfigBackfillRows{
			id:             9,
			installationID: 42,
			key:            "account",
			valueJSON:      legacy,
		},
		updateTag: pgconn.NewCommandTag("UPDATE 0"),
	}

	updated, err := backfillEncryptedConfigs(context.Background(), db, cipher)
	if err != nil {
		t.Fatalf("backfillEncryptedConfigs: %v", err)
	}
	if updated != 0 {
		t.Fatalf("updated = %d, want 0 after concurrent replacement", updated)
	}
	if !strings.Contains(db.updateSQL, "config_value = $3::jsonb") {
		t.Fatalf("query predicate missing: %s", db.updateSQL)
	}
	if len(db.updateArgs) != 3 || !bytes.Equal(db.updateArgs[2].([]byte), legacy) {
		t.Fatal("third query argument mismatch")
	}
}
