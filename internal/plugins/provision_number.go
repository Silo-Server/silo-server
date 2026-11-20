package plugins

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// Plugin RPCs use protobuf Struct, which represents every JSON number as a
// double. Preserve integers exactly and decimal fractions across the same
// shortest-decimal round trip used when clients encode them back to JSON.
func provisionNumberFitsRuntime(text string) bool {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return false
	}
	input, ok := new(big.Rat).SetString(text)
	if !ok {
		return false
	}
	if input.IsInt() {
		return input.Cmp(new(big.Rat).SetFloat64(value)) == 0
	}
	wire, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	return input.Cmp(wire) == 0
}

type provisionNumberError struct{ path string }

func (e *provisionNumberError) Error() string { return "unsupported plugin number at " + e.path }

func checkProvisionNumbers(path string, value any) error {
	switch value := value.(type) {
	case json.Number:
		if !provisionNumberFitsRuntime(value.String()) {
			return &provisionNumberError{path: path}
		}
	case map[string]any:
		for name, child := range value {
			if err := checkProvisionNumbers(path+"."+name, child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range value {
			if err := checkProvisionNumbers(path+"["+strconv.Itoa(i)+"]", child); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProvisionConfig(manifest *pluginv1.PluginManifest, key string, merged, fields map[string]any) error {
	projection := globalConfigValidationProjection(manifest, key, merged, fields)
	if err := ValidateGlobalConfigValue(manifest, key, projection); err != nil {
		return err
	}
	return checkProvisionNumbers("", projection)
}
