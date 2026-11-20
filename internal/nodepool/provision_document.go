package nodepool

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/provisiondoc"
)

// The variables an API server reads its transcode node declaration from. Set
// one or neither: the inline form takes JSON, the file YAML or JSON.
const (
	EnvTranscodeNodes     = "SILO_TRANSCODE_NODES"
	EnvTranscodeNodesFile = "SILO_TRANSCODE_NODES_FILE"
)

// MaxProvisionedNodeID leaves the identity sequence one value past the
// largest id a declaration can name, so creating that node never exhausts it.
const MaxProvisionedNodeID = MaxNodeID - 1

// The admin API's bounds on a node's name and URL.
const (
	maxNodeNameBytes = 1024
	maxNodeURLBytes  = 8192
)

// ProvisionedNode is one transcode node the deployment declares: the row it
// names by its key's id, and the name and URL that row is created with.
type ProvisionedNode struct {
	ID   int
	Name string
	URL  string
	Key  string
}

// NodeProvisioning is the transcode node declaration an API server read from
// its environment. Source names the variable it came from, for messages.
type NodeProvisioning struct {
	Source string
	Nodes  []ProvisionedNode
}

type nodeDocument struct {
	APIVersion *int                  `json:"apiVersion"`
	Nodes      map[string]*nodeEntry `json:"nodes"`
}

type nodeEntry struct {
	URL string          `json:"url"`
	Key *nodeSecretFrom `json:"key"`
}

type nodeSecretFrom struct {
	FromFile string `json:"fromFile"`
	FromEnv  string `json:"fromEnv"`
}

// ReadNodeProvisioning reads SILO_TRANSCODE_NODES or SILO_TRANSCODE_NODES_FILE
// and every key the declaration references. It returns nil when neither is
// set: that server leaves deployment-managed nodes as they are. A blank value
// counts as unset. Errors name the variable and the node, never a key.
func ReadNodeProvisioning(getenv func(string) string) (*NodeProvisioning, error) {
	inline := strings.TrimSpace(getenv(EnvTranscodeNodes))
	path := strings.TrimSpace(getenv(EnvTranscodeNodesFile))
	var (
		data   []byte
		source string
	)
	switch {
	case inline != "" && path != "":
		return nil, fmt.Errorf("set only one of %s and %s", EnvTranscodeNodes, EnvTranscodeNodesFile)
	case path != "":
		source = EnvTranscodeNodesFile
		file, err := provisiondoc.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		data = file
	case inline != "":
		source, data = EnvTranscodeNodes, []byte(inline)
	default:
		return nil, nil
	}
	nodes, err := parseNodeDocument(data, source == EnvTranscodeNodesFile, getenv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &NodeProvisioning{Source: source, Nodes: nodes}, nil
}

func parseNodeDocument(data []byte, allowYAML bool, getenv func(string) string) ([]ProvisionedNode, error) {
	if len(data) > provisiondoc.MaxBytes || !utf8.Valid(data) {
		return nil, errors.New("expected UTF-8 input up to 1 MiB")
	}
	if allowYAML && !jsontext.Value(data).IsValid() {
		var err error
		if data, err = provisiondoc.YAMLToJSON(data, provisiondoc.MaxBytes, nil); err != nil {
			return nil, err
		}
	}
	// The JSON decoder keeps the last of two equal keys; this refuses them.
	if !jsontext.Value(data).IsValid() {
		return nil, errors.New("invalid JSON: check syntax and duplicate keys")
	}
	var document nodeDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, nodeDocumentDecodeError(err)
	}
	if document.APIVersion == nil || *document.APIVersion != 1 {
		return nil, errors.New("apiVersion must be 1")
	}
	if document.Nodes == nil {
		return nil, errors.New("nodes is required; an empty map releases every node")
	}
	nodes := make([]ProvisionedNode, 0, len(document.Nodes))
	byID := make(map[int]string, len(document.Nodes))
	byURL := make(map[string]string, len(document.Nodes))
	var secretBytes int
	for _, name := range slices.Sorted(maps.Keys(document.Nodes)) {
		node, err := provisionedNode(name, document.Nodes[name], getenv, &secretBytes)
		if err != nil {
			return nil, fmt.Errorf("nodes.%s: %w", name, err)
		}
		if other, taken := byID[node.ID]; taken {
			return nil, fmt.Errorf("nodes.%s: its key names node %d, as nodes.%s's does", name, node.ID, other)
		}
		byID[node.ID] = name
		canonical := CanonicalNodeURL(node.URL)
		if other, taken := byURL[canonical]; taken {
			return nil, fmt.Errorf("nodes.%s: url is the same address as nodes.%s's", name, other)
		}
		byURL[canonical] = name
		nodes = append(nodes, node)
	}
	slices.SortFunc(nodes, func(a, b ProvisionedNode) int { return a.ID - b.ID })
	return nodes, nil
}

// nodeDocumentDecodeError describes a decode failure without its value: a
// decode error can quote one, and a key placed inline where a reference
// belongs is a value. It names the field only.
func nodeDocumentDecodeError(err error) error {
	typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err)
	switch {
	case ok && strings.HasSuffix(typeErr.Field, ".key"):
		return fmt.Errorf("%s must be { fromFile: <path> } or { fromEnv: <variable> }, not the key itself", typeErr.Field)
	case ok && typeErr.Field != "":
		return fmt.Errorf("unexpected value at %s", typeErr.Field)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return errors.New(strings.TrimPrefix(err.Error(), "json: "))
	default:
		return errors.New("not a transcode node declaration")
	}
}

func provisionedNode(name string, entry *nodeEntry, getenv func(string) string, secretBytes *int) (ProvisionedNode, error) {
	if strings.TrimSpace(name) != name || name == "" || len(name) > maxNodeNameBytes {
		return ProvisionedNode{}, fmt.Errorf("the name must be 1 to %d bytes without surrounding spaces", maxNodeNameBytes)
	}
	if entry == nil {
		return ProvisionedNode{}, errors.New("url and key are required")
	}
	nodeURL := strings.TrimSpace(entry.URL)
	if len(nodeURL) > maxNodeURLBytes || !absoluteHTTPURL(nodeURL) {
		return ProvisionedNode{}, fmt.Errorf("url %s", detailAbsoluteURL)
	}
	key, err := readNodeSecret(entry.Key, getenv, secretBytes)
	if err != nil {
		return ProvisionedNode{}, fmt.Errorf("key: %w", err)
	}
	id, err := ParseNodeKey(key)
	if err != nil {
		return ProvisionedNode{}, errors.New("key: not a node key (snk1_<id>_<43 base64url characters>)")
	}
	if id > MaxProvisionedNodeID {
		return ProvisionedNode{}, fmt.Errorf("key: names node %d; a declared node's id must be at most %d", id, MaxProvisionedNodeID)
	}
	return ProvisionedNode{ID: id, Name: name, URL: nodeURL, Key: key}, nil
}

// readNodeSecret reads a key from the file or variable a reference names. A
// key is never written into the declaration itself, so the declaration can
// live where secrets do not.
func readNodeSecret(ref *nodeSecretFrom, getenv func(string) string, total *int) (string, error) {
	if ref == nil || (ref.FromFile == "") == (ref.FromEnv == "") {
		return "", errors.New("must be { fromFile: <path> } or { fromEnv: <variable> }")
	}
	var secret string
	if ref.FromFile != "" {
		var err error
		if secret, err = provisiondoc.ReadSecretFile(ref.FromFile); err != nil {
			return "", fmt.Errorf("fromFile: %w", err)
		}
	} else {
		secret = getenv(ref.FromEnv)
	}
	// As the node reads its own SILO_NODE_KEY.
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", errors.New("the referenced value is empty")
	}
	if len(secret) > provisiondoc.MaxBytes-*total {
		return "", errors.New("referenced keys exceed 1 MiB together")
	}
	*total += len(secret)
	return secret, nil
}
