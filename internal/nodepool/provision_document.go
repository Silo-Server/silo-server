package nodepool

import (
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
		// The document has no fractional numbers, and yaml.v3 rounds a long
		// one: 1.00000000000000001 would pass as apiVersion 1.
		noFloats := func(string) bool { return false }
		var err error
		if data, err = provisiondoc.YAMLToJSON(data, provisiondoc.MaxBytes, noFloats); err != nil {
			return nil, err
		}
	}
	// The JSON decoder keeps the last of two equal keys; this refuses them.
	if !jsontext.Value(data).IsValid() {
		return nil, errors.New("invalid JSON: check syntax and duplicate keys")
	}
	document, err := exactMembers(data, "the declaration", "apiVersion", "nodes")
	if err != nil {
		return nil, err
	}
	if string(document["apiVersion"]) != "1" {
		return nil, errors.New("apiVersion must be 1")
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(document["nodes"], &entries); err != nil || entries == nil {
		return nil, errors.New("nodes is required, as a map of node names; an empty map releases every node")
	}
	nodes := make([]ProvisionedNode, 0, len(entries))
	byID := make(map[int]string, len(entries))
	byURL := make(map[string]string, len(entries))
	var secretBytes int
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		node, err := provisionedNode(name, entries[name], getenv, &secretBytes)
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

// exactMembers decodes a JSON object whose member names must be among
// allowed, compared exactly: the standard decoder matches struct fields
// without regard to case, so "Nodes" would stand in for "nodes". The error
// never names an unknown member, which could be a key pasted in the wrong
// place.
func exactMembers(raw []byte, what string, allowed ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", what)
	}
	for name := range object {
		if !slices.Contains(allowed, name) {
			return nil, fmt.Errorf("%s has a field other than %s", what, strings.Join(allowed, ", "))
		}
	}
	return object, nil
}

func provisionedNode(name string, raw json.RawMessage, getenv func(string) string, secretBytes *int) (ProvisionedNode, error) {
	if strings.TrimSpace(name) != name || name == "" || len(name) > maxNodeNameBytes {
		return ProvisionedNode{}, fmt.Errorf("the name must be 1 to %d bytes without surrounding spaces", maxNodeNameBytes)
	}
	entry, err := exactMembers(raw, "the entry", "url", "key")
	if err != nil {
		return ProvisionedNode{}, err
	}
	nodeURL, ok := optionalString(entry["url"])
	if !ok {
		return ProvisionedNode{}, errors.New("url must be a string")
	}
	nodeURL = strings.TrimSpace(nodeURL)
	if len(nodeURL) > maxNodeURLBytes || !absoluteHTTPURL(nodeURL) {
		return ProvisionedNode{}, fmt.Errorf("url %s", detailAbsoluteURL)
	}
	key, err := readNodeSecret(entry["key"], getenv, secretBytes)
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
func readNodeSecret(raw json.RawMessage, getenv func(string) string, total *int) (string, error) {
	const form = "must be { fromFile: <path> } or { fromEnv: <variable> }"
	var inline string
	if json.Unmarshal(raw, &inline) == nil {
		return "", errors.New(form + ", not the key itself")
	}
	ref, err := exactMembers(raw, "the reference", "fromFile", "fromEnv")
	if err != nil {
		return "", errors.New(form)
	}
	fromFile, fileOK := optionalString(ref["fromFile"])
	fromEnv, envOK := optionalString(ref["fromEnv"])
	if !fileOK || !envOK || (fromFile == "") == (fromEnv == "") {
		return "", errors.New(form)
	}
	var secret string
	if fromFile != "" {
		if secret, err = provisiondoc.ReadSecretFile(fromFile); err != nil {
			return "", fmt.Errorf("fromFile: %w", err)
		}
	} else {
		secret = getenv(fromEnv)
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

// optionalString decodes a member that is absent or a string.
func optionalString(raw json.RawMessage) (string, bool) {
	var value string
	if raw != nil && json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}
