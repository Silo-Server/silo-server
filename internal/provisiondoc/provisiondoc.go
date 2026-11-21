// Package provisiondoc reads the deployment documents an API server takes
// from its environment (SILO_PLUGINS, SILO_TRANSCODE_NODES): a bounded
// regular-file read, a YAML reader that converts to JSON without echoing the
// input, and secret files referenced from a document.
package provisiondoc

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Silo-Server/silo-server/internal/envutil"
)

// MaxBytes bounds a document, a file it is read from, and the secrets it
// references together.
const MaxBytes = 1 << 20

// ReadFile reads a regular file of at most MaxBytes. Its errors never include
// the file's content.
func ReadFile(path string) ([]byte, error) {
	return envutil.ReadRegularFile(path)
}

// ReadSecretFile reads a secret from a mounted file, without the one line
// ending most secret stores and editors leave after the value.
func ReadSecretFile(path string) (string, error) {
	return envutil.ReadSecretFile(path)
}

// ErrExpandedSize reports a YAML document whose aliases expand past the limit.
var ErrExpandedSize = errors.New("expanded declaration exceeds 1 MiB")

// YAMLToJSON converts one YAML document of JSON-compatible values to JSON,
// charging aliases for what they expand to and refusing duplicate mapping
// keys, aliased ones included. fitsNumber, when set, vets each floating point
// scalar's spelling before yaml.v3 converts it. Errors never quote the input,
// which may hold secrets.
func YAMLToJSON(data []byte, limit int, fitsNumber func(string) bool) ([]byte, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, errors.New("invalid provisioning YAML: check syntax and duplicate keys")
	}
	if err := decoder.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return nil, errors.New("provisioning expects one YAML document")
	}
	// yaml.v3 decodes floating point scalars into float64. Check the spelling
	// before that conversion so a lossy value cannot pass the JSON checks.
	if _, err := yamlSize(&root, make(map[*yaml.Node]int), limit, fitsNumber); err != nil {
		return nil, err
	}
	var document any
	if err := root.Decode(&document); err != nil {
		return nil, errors.New("invalid provisioning YAML: check syntax and duplicate keys")
	}
	if !jsonValue(document) {
		return nil, errors.New("provisioning requires JSON-compatible YAML with string mapping keys")
	}
	encoded := jsonBuffer{limit: limit}
	if err := jsonv2.MarshalWrite(&encoded, document, jsonv2.WithMarshalers(yamlFloat)); err != nil {
		if errors.Is(err, ErrExpandedSize) {
			return nil, ErrExpandedSize
		}
		return nil, errors.New("provisioning requires finite JSON numbers and UTF-8 strings")
	}
	return encoded.Bytes(), nil
}

type jsonBuffer struct {
	bytes.Buffer
	limit int
}

func (b *jsonBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrExpandedSize
	}
	return b.Buffer.Write(p)
}

// The shortest decimal for an integral float can name a different integer
// that rounds to the same float. Preserve its exact integer through JSON.
var yamlFloat = jsonv2.MarshalFunc(func(value float64) ([]byte, error) {
	if value == math.Trunc(value) && !math.IsInf(value, 0) {
		return strconv.AppendFloat(nil, value, 'f', 0, 64), nil
	}
	return json.Marshal(value)
})

// Charge each alias for its target before yaml.v3 expands it. Only completed
// anchors can be referenced, so an alias back to a parent is invalid.
func yamlSize(node *yaml.Node, anchors map[*yaml.Node]int, limit int, fitsNumber func(string) bool) (int, error) {
	if node.Kind == yaml.AliasNode {
		size, ok := anchors[node.Alias]
		if !ok {
			return 0, errors.New("recursive or unresolved YAML alias")
		}
		return size, nil
	}
	if fitsNumber != nil && node.Kind == yaml.ScalarNode && node.ShortTag() == "!!float" &&
		!fitsNumber(strings.ReplaceAll(node.Value, "_", "")) {
		return 0, fmt.Errorf("unsupported number at YAML line %d, column %d", node.Line, node.Column)
	}
	size := max(1, len(node.Value))
	var keys map[string]struct{}
	if node.Kind == yaml.MappingNode {
		keys = make(map[string]struct{}, len(node.Content)/2)
	}
	for i, child := range node.Content {
		n, err := yamlSize(child, anchors, limit, fitsNumber)
		if err != nil {
			return 0, err
		}
		size += n
		if size > limit {
			return 0, ErrExpandedSize
		}
		if keys != nil && i%2 == 0 {
			key := child
			if key.Kind == yaml.AliasNode {
				key = key.Alias
			}
			if key.Kind != yaml.ScalarNode || (key.ShortTag() != "!!str" && key.ShortTag() != "!!merge") {
				return 0, errors.New("provisioning requires string mapping keys")
			}
			if _, duplicate := keys[key.Value]; duplicate {
				return 0, fmt.Errorf("duplicate YAML mapping key at line %d, column %d", child.Line, child.Column)
			}
			keys[key.Value] = struct{}{}
		}
	}
	if node.Anchor != "" {
		anchors[node] = size
	}
	return size, nil
}

func jsonValue(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for _, item := range value {
			if !jsonValue(item) {
				return false
			}
		}
	case []any:
		for _, item := range value {
			if !jsonValue(item) {
				return false
			}
		}
	case nil, string, bool, int, int64, uint64, float64:
	default:
		return false
	}
	return true
}
