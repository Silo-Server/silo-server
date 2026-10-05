package contract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

const contractSchemaRoot = "../../../docs/design/schemas/client-diagnostics"

func TestValidFixtures(t *testing.T) {
	fixtures := mustGlob(t, "v1/fixtures/valid/*")
	if len(fixtures) == 0 {
		t.Fatal("valid fixtures missing")
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			data := mustReadFixture(t, fixture)
			switch {
			case strings.HasSuffix(fixture, "device.json"):
				if _, err := ValidateDevice(data); err != nil {
					t.Fatalf("ValidateDevice() error = %v", err)
				}
			case strings.HasSuffix(fixture, ".jsonl"):
				validateLogLinesFixture(t, data)
			case strings.HasSuffix(fixture, ".json"):
				if _, err := ValidateManifest(data); err != nil {
					t.Fatalf("ValidateManifest() error = %v", err)
				}
			default:
				t.Fatalf("unexpected fixture extension: %s", fixture)
			}
		})
	}
}

func TestInvalidManifestFixtures(t *testing.T) {
	fixtures := mustGlob(t, "v1/fixtures/invalid/*.json")
	if len(fixtures) < 6 {
		t.Fatalf("invalid fixtures = %d, want at least 6", len(fixtures))
	}

	seenErrors := map[string]string{}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			_, err := ValidateManifest(mustReadFixture(t, fixture))
			if err == nil {
				t.Fatal("ValidateManifest() error = nil, want failure")
			}
			msg := err.Error()
			if prior := seenErrors[msg]; prior != "" {
				t.Fatalf("error %q also used by %s", msg, prior)
			}
			seenErrors[msg] = filepath.Base(fixture)
		})
	}
}

func TestValidateManifestRequiresCrashForEventReports(t *testing.T) {
	// A non-manual report with the crash object stripped must be rejected so
	// event reports always carry crash summary/source/occurred_at.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mustReadFixture(t, "v1/fixtures/valid/android-tv-crash-ueh.json"), &m); err != nil {
		t.Fatalf("parse crash fixture: %v", err)
	}
	delete(m, "crash")
	stripped, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal stripped manifest: %v", err)
	}
	_, err = ValidateManifest(stripped)
	if err == nil || !strings.Contains(err.Error(), "crash") {
		t.Fatalf("ValidateManifest() error = %v, want crash required for event report", err)
	}
}

func TestValidateManifestRejectsCrashOnManualReports(t *testing.T) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mustReadFixture(t, "v1/fixtures/valid/android-tv-crash-ueh.json"), &m); err != nil {
		t.Fatalf("parse crash fixture: %v", err)
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(m["report"], &report); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	report["type"] = json.RawMessage(`"manual"`)
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	m["report"] = reportJSON
	withCrash, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	_, err = ValidateManifest(withCrash)
	if err == nil || !strings.Contains(err.Error(), "crash") {
		t.Fatalf("ValidateManifest() error = %v, want crash rejected for manual report", err)
	}
}

func TestValidateManifestOccurrenceCount(t *testing.T) {
	withCount := func(t *testing.T, count string) []byte {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal(mustReadFixture(t, "v1/fixtures/valid/android-tv-crash-ueh.json"), &m); err != nil {
			t.Fatalf("parse crash fixture: %v", err)
		}
		var report map[string]json.RawMessage
		if err := json.Unmarshal(m["report"], &report); err != nil {
			t.Fatalf("parse report: %v", err)
		}
		if count == "" {
			delete(report, "occurrence_count")
		} else {
			report["occurrence_count"] = json.RawMessage(count)
		}
		reportJSON, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		m["report"] = reportJSON
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		return data
	}

	for _, tc := range []struct {
		name  string
		count string
		want  int
	}{
		{name: "absent means one occurrence", count: "", want: 0},
		{name: "one", count: "1", want: 1},
		{name: "repeats", count: "42", want: 42},
		{name: "maximum", count: "1000000", want: MaxOccurrenceCount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := ValidateManifest(withCount(t, tc.count))
			if err != nil {
				t.Fatalf("ValidateManifest() error = %v", err)
			}
			if manifest.Report.OccurrenceCount != tc.want {
				t.Fatalf("OccurrenceCount = %d, want %d", manifest.Report.OccurrenceCount, tc.want)
			}
		})
	}

	for _, count := range []string{"0", "-1", "1000001", "1.5", `"3"`} {
		t.Run("rejects "+count, func(t *testing.T) {
			if _, err := ValidateManifest(withCount(t, count)); err == nil {
				t.Fatalf("ValidateManifest() error = nil for occurrence_count %s", count)
			}
		})
	}
}

func TestSchemaEnumsAndRequiredFieldsStayInSync(t *testing.T) {
	manifest := mustReadObject(t, "v1/manifest.schema.json")
	assertStringsEqual(t, "manifest.required", schemaStrings(t, manifest, "required"), manifestRequiredFields)
	assertConstInt(t, "manifest.schema_version.const", schemaValue(t, manifest, "properties", "schema_version", "const"), SchemaVersion)
	assertConstInt(t, "manifest.report.occurrence_count.maximum", schemaValue(t, manifest, "properties", "report", "properties", "occurrence_count", "maximum"), MaxOccurrenceCount)
	assertStringsEqual(t, "manifest.report.required", schemaStrings(t, manifest, "properties", "report", "required"), manifestReportRequiredFields)
	assertStringsEqual(t, "manifest.destination.required", schemaStrings(t, manifest, "properties", "destination", "required"), manifestDestinationRequiredFields)
	assertStringsEqual(t, "manifest.consent.required", schemaStrings(t, manifest, "properties", "consent", "required"), manifestConsentRequiredFields)
	assertStringsEqual(t, "manifest.crash.required", schemaStrings(t, manifest, "properties", "crash", "required"), manifestCrashRequiredFields)
	assertStringsEqual(t, "manifest.device_summary.required", schemaStrings(t, manifest, "properties", "device_summary", "required"), manifestDeviceSummaryFields)
	assertStringsEqual(t, "manifest.log_summary.required", schemaStrings(t, manifest, "properties", "log_summary", "required"), manifestLogSummaryRequiredFields)
	assertStringsEqual(t, "manifest.archive.required", schemaStrings(t, manifest, "properties", "archive", "required"), manifestArchiveRequiredFields)
	assertStringsEqual(t, "manifest.report.type.enum", schemaStrings(t, manifest, "properties", "report", "properties", "type", "enum"), reportTypes)
	assertStringsEqual(t, "manifest.report.platform.enum", schemaStrings(t, manifest, "properties", "report", "properties", "platform", "enum"), platforms)
	assertStringsEqual(t, "manifest.consent.mode.enum", schemaStrings(t, manifest, "properties", "consent", "properties", "mode", "enum"), consentModes)
	assertStringsEqual(t, "manifest.crash.source.enum", schemaStrings(t, manifest, "properties", "crash", "properties", "source", "enum"), crashSources)
	assertStringsEqual(t, "manifest.crash.provenance.enum", schemaStrings(t, manifest, "properties", "crash", "properties", "provenance", "enum"), crashProvenances)
	assertStringsEqual(t, "manifest.log_summary.categories.enum", schemaStrings(t, manifest, "properties", "log_summary", "properties", "categories", "items", "enum"), logCategories)
	assertStringsEqual(t, "manifest.archive.entries.enum", schemaStrings(t, manifest, "properties", "archive", "properties", "entries", "items", "enum"), ArchiveEntryAllowlist)

	logline := mustReadObject(t, "v1/logline.schema.json")
	assertStringsEqual(t, "logline.required", schemaStrings(t, logline, "required"), []string{"ts", "run", "lvl", "cat", "tag", "msg"})
	assertStringsEqual(t, "logline.lvl.enum", schemaStrings(t, logline, "properties", "lvl", "enum"), logLevels)
	assertStringsEqual(t, "logline.cat.enum", schemaStrings(t, logline, "properties", "cat", "enum"), logCategories)

	device := mustReadObject(t, "v1/device.schema.json")
	assertStringsEqual(t, "device.required", schemaStrings(t, device, "required"), deviceRequiredFields)
	assertStringsEqual(t, "device.provenance.enum", schemaStrings(t, device, "properties", "provenance", "enum"), deviceProvenances)
}

func TestAttrRegistryStaysInSync(t *testing.T) {
	var registry struct {
		Categories map[string]map[string]struct {
			Type string `json:"type"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(mustReadFixture(t, "v1/attr-registry.json"), &registry); err != nil {
		t.Fatalf("parse attr-registry.json: %v", err)
	}

	got := make(map[string]map[string]string, len(registry.Categories))
	for category, keys := range registry.Categories {
		got[category] = make(map[string]string, len(keys))
		for key, spec := range keys {
			got[category][key] = spec.Type
		}
	}
	want := make(map[string]map[string]string, len(attrRegistry))
	for category, keys := range attrRegistry {
		want[category] = make(map[string]string, len(keys))
		for key, valueType := range keys {
			want[category][key] = string(valueType)
		}
	}

	if !mapsEqual(got, want) {
		t.Fatalf("attr registry mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestValidateLogLineDropsUnregisteredAttrs(t *testing.T) {
	line := []byte(`{"ts":"2026-07-19T18:22:29Z","run":"run_1","lvl":"I","cat":"playback","tag":"Player","msg":"started","attrs":{"sink":"HDMI","unregistered":"drop-me"}}`)
	got, err := ValidateLogLine(line)
	if err != nil {
		t.Fatalf("ValidateLogLine() error = %v", err)
	}
	if _, ok := got.Attrs["sink"]; !ok {
		t.Fatalf("registered attr missing: %#v", got.Attrs)
	}
	if _, ok := got.Attrs["unregistered"]; ok {
		t.Fatalf("unregistered attr was not dropped: %#v", got.Attrs)
	}
}

func TestValidateLogLineRejectsRegisteredAttrTypeMismatch(t *testing.T) {
	line := []byte(`{"ts":"2026-07-19T18:22:29Z","run":"run_1","lvl":"I","cat":"network","tag":"HTTP","msg":"done","attrs":{"status":"200"}}`)
	if _, err := ValidateLogLine(line); err == nil {
		t.Fatal("ValidateLogLine() error = nil, want type mismatch")
	}
}

func validateLogLinesFixture(t *testing.T, data []byte) {
	t.Helper()

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)
	lineCount := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lineCount++
		if _, err := ValidateLogLine(line); err != nil {
			t.Fatalf("line %d: ValidateLogLine() error = %v", lineCount, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan loglines fixture: %v", err)
	}
	if lineCount == 0 {
		t.Fatal("loglines fixture has no log lines")
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(contractSchemaRoot, filepath.FromSlash(pattern)))
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	for i, match := range matches {
		rel, err := filepath.Rel(contractSchemaRoot, match)
		if err != nil {
			t.Fatalf("rel %s: %v", match, err)
		}
		matches[i] = filepath.ToSlash(rel)
	}
	sort.Strings(matches)
	return matches
}

func mustReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractSchemaRoot, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func mustReadObject(t *testing.T, path string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(mustReadFixture(t, path), &obj); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return obj
}

func schemaStrings(t *testing.T, root any, path ...string) []string {
	t.Helper()
	value := schemaValue(t, root, path...)
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is %T, want array", strings.Join(path, "."), value)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("%s item is %T, want string", strings.Join(path, "."), item)
		}
		out = append(out, s)
	}
	return out
}

func schemaValue(t *testing.T, root any, path ...string) any {
	t.Helper()
	current := root
	for _, key := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s parent is %T, want object", key, current)
		}
		next, ok := obj[key]
		if !ok {
			t.Fatalf("missing schema path %s", strings.Join(path, "."))
		}
		current = next
	}
	return current
}

func assertConstInt(t *testing.T, label string, got any, want int) {
	t.Helper()
	gotFloat, ok := got.(float64)
	if !ok {
		t.Fatalf("%s = %T, want JSON number", label, got)
	}
	if int(gotFloat) != want || gotFloat != float64(want) {
		t.Fatalf("%s = %v, want %d", label, got, want)
	}
}

func assertStringsEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s mismatch\ngot:  %v\nwant: %v", label, got, want)
	}
}

func mapsEqual(got, want map[string]map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for category, gotKeys := range got {
		wantKeys, ok := want[category]
		if !ok || len(gotKeys) != len(wantKeys) {
			return false
		}
		for key, gotType := range gotKeys {
			if wantKeys[key] != gotType {
				return false
			}
		}
	}
	return true
}
