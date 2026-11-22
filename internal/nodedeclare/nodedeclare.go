// Package nodedeclare reads the configuration a stream node declares about
// itself from its environment. A proxy writes its declaration to its own
// stream_nodes row; a transcode node sends it to the core over the node
// control plane. Either way the fields it names are locked against
// administrator edits until the node starts without them.
package nodedeclare

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// Variable is one environment variable a node reads its declaration from.
type Variable struct {
	// Name is the environment variable.
	Name string
	// Field is the configuration field it declares (nodepool.Field*).
	Field string
	set   func(in *nodepool.UpdateNodeInput, value string) error
}

func text(field string) func(*nodepool.UpdateNodeInput, string) error {
	return func(in *nodepool.UpdateNodeInput, value string) error {
		v := new(value)
		switch field {
		case nodepool.FieldName:
			in.Name = v
		case nodepool.FieldURL:
			in.URL = v
		case nodepool.FieldPublicURL:
			in.PublicURL = v
		case nodepool.FieldGroup:
			in.Group = v
		case nodepool.FieldSourceAccess:
			in.SourceAccess = new(strings.ToLower(value))
		case nodepool.FieldHWAccelOverride:
			in.HWAccelOverride = new(strings.ToLower(value))
		case nodepool.FieldHWDeviceOverride:
			in.HWDeviceOverride = v
		case nodepool.FieldDownloadArtifactDirOverride:
			in.DownloadArtifactDirOverride = v
		}
		return nil
	}
}

// count reads a cap: a whole number, 0 for unlimited.
func count(field string) func(*nodepool.UpdateNodeInput, string) error {
	return func(in *nodepool.UpdateNodeInput, value string) error {
		n, err := strconv.ParseInt(value, 10, 32)
		if err != nil || n < 0 {
			return errors.New("must be a whole number, 0 for unlimited")
		}
		v := new(int(n))
		switch field {
		case nodepool.FieldMaxJobs:
			in.MaxJobs = v
		case nodepool.FieldMaxBandwidthKbps:
			in.MaxBandwidthKbps = v
		}
		return nil
	}
}

func byteBudget(in *nodepool.UpdateNodeInput, value string) error {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return errors.New("must be a whole number of bytes, 0 for no budget")
	}
	in.DownloadArtifactMaxBytesOverride = &n
	return nil
}

// The variables each node type reads. SILO_NODE_GROUP means the same on both.
var (
	proxyVariables = []Variable{
		{Name: "SILO_NODE_PUBLIC_URL", Field: nodepool.FieldPublicURL, set: text(nodepool.FieldPublicURL)},
		{Name: "SILO_NODE_GROUP", Field: nodepool.FieldGroup, set: text(nodepool.FieldGroup)},
		{Name: "SILO_NODE_MAX_STREAMS", Field: nodepool.FieldMaxJobs, set: count(nodepool.FieldMaxJobs)},
		{Name: "SILO_NODE_MAX_BANDWIDTH_KBPS", Field: nodepool.FieldMaxBandwidthKbps, set: count(nodepool.FieldMaxBandwidthKbps)},
	}
	transcodeVariables = []Variable{
		{Name: "SILO_NODE_NAME", Field: nodepool.FieldName, set: text(nodepool.FieldName)},
		{Name: "SILO_NODE_URL", Field: nodepool.FieldURL, set: text(nodepool.FieldURL)},
		{Name: "SILO_NODE_GROUP", Field: nodepool.FieldGroup, set: text(nodepool.FieldGroup)},
		{Name: "SILO_NODE_MAX_TRANSCODES", Field: nodepool.FieldMaxJobs, set: count(nodepool.FieldMaxJobs)},
		{Name: "SILO_NODE_SOURCE_ACCESS", Field: nodepool.FieldSourceAccess, set: text(nodepool.FieldSourceAccess)},
		{Name: "SILO_NODE_HW_ACCEL", Field: nodepool.FieldHWAccelOverride, set: text(nodepool.FieldHWAccelOverride)},
		{Name: "SILO_NODE_HW_DEVICE", Field: nodepool.FieldHWDeviceOverride, set: text(nodepool.FieldHWDeviceOverride)},
		{Name: "SILO_NODE_DOWNLOAD_ARTIFACT_DIR", Field: nodepool.FieldDownloadArtifactDirOverride, set: text(nodepool.FieldDownloadArtifactDirOverride)},
		{Name: "SILO_NODE_DOWNLOAD_ARTIFACT_MAX_BYTES", Field: nodepool.FieldDownloadArtifactMaxBytesOverride, set: byteBudget},
	}
)

// Variables lists the variables a node of nodeType reads.
func Variables(nodeType string) []Variable {
	switch nodeType {
	case nodepool.NodeTypeProxy:
		return proxyVariables
	case nodepool.NodeTypeTranscode:
		return transcodeVariables
	}
	return nil
}

// VariableFor names the variable that declares field on a node of nodeType,
// or "" when none does.
func VariableFor(nodeType, field string) string {
	for _, v := range Variables(nodeType) {
		if v.Field == field {
			return v.Name
		}
	}
	return ""
}

// Error refuses one variable's value.
type Error struct {
	Variable string
	Detail   string
}

func (e *Error) Error() string { return e.Variable + " " + e.Detail }

// FromEnv reads the declaration of a node of nodeType. A variable that is
// unset, or set to nothing but spaces, is not declared and leaves its field to
// the administrator. Each value is checked with the rules the core applies, so
// a typo fails here, naming its variable.
func FromEnv(nodeType string, getenv func(string) string) (nodepool.UpdateNodeInput, error) {
	var in nodepool.UpdateNodeInput
	for _, v := range Variables(nodeType) {
		value := strings.TrimSpace(getenv(v.Name))
		if value == "" {
			continue
		}
		if err := v.set(&in, value); err != nil {
			return nodepool.UpdateNodeInput{}, &Error{Variable: v.Name, Detail: err.Error()}
		}
	}
	if err := nodepool.ValidateDeclaration(nodeType, in); err != nil {
		if declErr, ok := errors.AsType[*nodepool.DeclarationError](err); ok {
			return nodepool.UpdateNodeInput{}, &Error{Variable: VariableFor(nodeType, declErr.Field), Detail: declErr.Detail}
		}
		return nodepool.UpdateNodeInput{}, fmt.Errorf("node declaration: %w", err)
	}
	return in, nil
}
