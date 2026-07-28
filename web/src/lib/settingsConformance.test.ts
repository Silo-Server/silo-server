import { describe, expect, it } from "vitest";

// ?raw keeps JSON.parse in this file's hands: a plain JSON import would let the
// bundler normalize the document before the strict field check ever sees it.
import conformanceRaw from "./settingsConformance.json?raw";

import { SETTINGS_REVISION } from "./settingsContract";
import {
  jsonEquals,
  resolveSettingValues,
  type RemoteSettingScope,
  type SettingConstraintBinding,
  type SettingConstraintKind,
  type SettingResolutionContext,
  type StoredSettingRow,
} from "./settingsResolve";

/**
 * The web runner for the cross-platform conformance fixture — the spec's named
 * drift gate. The vendored copy read here is written by `make settings-bindings`
 * from contracts/settings/v1/conformance.json (and `make
 * verify-settings-bindings` fails CI when the two disagree). The Go resolver
 * runs the identical cases in internal/settingsresolve/conformance_test.go;
 * Kotlin and Swift runners follow in the client repos. The fixture is decoded
 * strictly: a field this runner does not recognize fails the suite, because
 * schema drift in the fixture itself is drift.
 */

interface ConformanceFixture {
  fixture_version: number;
  manifest_revision: number;
  description: string;
  cases: ConformanceCase[];
}

interface ConformanceCase {
  name: string;
  description?: string;
  keys: string[];
  context?: {
    profile_id?: string;
    device_id?: string;
    library_ids?: number[];
    series_ids?: string[];
  };
  stored?: {
    key: string;
    scope: RemoteSettingScope;
    profile_id?: string;
    device_id?: string;
    library_id?: number;
    series_id?: string;
    value: unknown;
  }[];
  constraints?: Record<string, unknown>;
  constraint_bindings?: {
    key: string;
    policy_input: string;
    constraint: SettingConstraintKind;
  }[];
  expected: {
    key: string;
    value: unknown;
    source: string;
    constrained?: boolean;
    stored_value?: unknown;
    constraint_kind?: string;
  }[];
}

/** Fails on any object key outside `allowed` — the unknown-field gate. */
function assertOnlyKnownFields(value: unknown, allowed: readonly string[], path: string): void {
  expect(value, path).toBeTypeOf("object");
  expect(value, path).not.toBeNull();
  expect(Array.isArray(value), `${path} must be an object, not an array`).toBe(false);
  const unknown = Object.keys(value as Record<string, unknown>).filter(
    (key) => !allowed.includes(key),
  );
  expect(unknown, `unknown fixture field(s) at ${path}`).toEqual([]);
}

function loadFixture(): ConformanceFixture {
  const fixture = JSON.parse(conformanceRaw) as unknown;

  assertOnlyKnownFields(
    fixture,
    ["fixture_version", "manifest_revision", "description", "cases"],
    "fixture",
  );
  const typed = fixture as ConformanceFixture;
  expect(Array.isArray(typed.cases), "fixture.cases must be an array").toBe(true);

  typed.cases.forEach((testCase, caseIndex) => {
    const casePath = `cases[${caseIndex}]`;
    assertOnlyKnownFields(
      testCase,
      [
        "name",
        "description",
        "keys",
        "context",
        "stored",
        "constraints",
        "constraint_bindings",
        "expected",
      ],
      casePath,
    );
    if (testCase.context !== undefined) {
      assertOnlyKnownFields(
        testCase.context,
        ["profile_id", "device_id", "library_ids", "series_ids"],
        `${casePath}.context`,
      );
    }
    testCase.stored?.forEach((row, rowIndex) => {
      const rowPath = `${casePath}.stored[${rowIndex}]`;
      assertOnlyKnownFields(
        row,
        ["key", "scope", "profile_id", "device_id", "library_id", "series_id", "value"],
        rowPath,
      );
      expect("value" in row, `${rowPath} must spell an authored null as null`).toBe(true);
    });
    testCase.constraint_bindings?.forEach((binding, bindingIndex) => {
      assertOnlyKnownFields(
        binding,
        ["key", "policy_input", "constraint"],
        `${casePath}.constraint_bindings[${bindingIndex}]`,
      );
    });
    testCase.expected.forEach((expectation, expectationIndex) => {
      const expectationPath = `${casePath}.expected[${expectationIndex}]`;
      assertOnlyKnownFields(
        expectation,
        ["key", "value", "source", "constrained", "stored_value", "constraint_kind"],
        expectationPath,
      );
      expect("value" in expectation, `${expectationPath} must spell an expected null as null`).toBe(
        true,
      );
    });
  });
  return typed;
}

const fixture = loadFixture();

describe("settings conformance fixture", () => {
  it("targets this build's manifest revision", () => {
    expect(fixture.fixture_version).toBe(1);
    // A revision bump changes definitions, so the fixture author must
    // re-confirm every expectation against the new manifest.
    expect(fixture.manifest_revision).toBe(SETTINGS_REVISION);
    expect(fixture.cases.length).toBeGreaterThan(0);
  });

  it("declares each case once", () => {
    const names = fixture.cases.map((testCase) => testCase.name);
    expect(new Set(names).size).toBe(names.length);
  });

  describe.each(fixture.cases.map((testCase) => [testCase.name, testCase] as const))(
    "%s",
    (_name, testCase) => {
      it("resolves to the expected effective values", () => {
        const context: SettingResolutionContext = {
          profileId: testCase.context?.profile_id,
          deviceId: testCase.context?.device_id,
          libraryIds: testCase.context?.library_ids,
          seriesIds: testCase.context?.series_ids,
        };
        const stored: StoredSettingRow[] = (testCase.stored ?? []).map((row) => ({
          key: row.key,
          scope: row.scope,
          profileId: row.profile_id,
          deviceId: row.device_id,
          libraryId: row.library_id,
          seriesId: row.series_id,
          value: row.value,
        }));
        const bindings: Record<string, SettingConstraintBinding> = {};
        for (const binding of testCase.constraint_bindings ?? []) {
          expect(bindings[binding.key], `duplicate binding for ${binding.key}`).toBeUndefined();
          bindings[binding.key] = {
            policyInput: binding.policy_input,
            constraint: binding.constraint,
          };
        }

        const resolved = resolveSettingValues(
          testCase.keys,
          stored,
          context,
          testCase.constraints,
          bindings,
        );

        expect(resolved).toHaveLength(testCase.expected.length);
        const byKey = new Map(resolved.map((entry) => [entry.key as string, entry]));
        for (const expectation of testCase.expected) {
          const entry = byKey.get(expectation.key);
          expect(entry, `no resolved value for ${expectation.key}`).toBeDefined();
          if (!entry) continue;
          expect(
            jsonEquals(entry.value, expectation.value),
            `${expectation.key}: value ${JSON.stringify(entry.value)}, want ${JSON.stringify(expectation.value)}`,
          ).toBe(true);
          expect(entry.source, `${expectation.key}: source`).toBe(expectation.source);
          expect(entry.constrained, `${expectation.key}: constrained`).toBe(
            expectation.constrained ?? false,
          );
          expect(entry.constraintKind, `${expectation.key}: constraint_kind`).toBe(
            expectation.constraint_kind,
          );
          if (expectation.constrained) {
            expect(
              "stored_value" in expectation,
              `${expectation.key}: a constrained expectation must declare stored_value`,
            ).toBe(true);
            expect(
              "constraint_kind" in expectation,
              `${expectation.key}: a constrained expectation must declare constraint_kind`,
            ).toBe(true);
            expect(
              jsonEquals(entry.storedValue, expectation.stored_value),
              `${expectation.key}: stored_value ${JSON.stringify(entry.storedValue)}, want ${JSON.stringify(expectation.stored_value)}`,
            ).toBe(true);
          } else {
            expect(
              "stored_value" in expectation,
              `${expectation.key}: an unconstrained expectation must not declare stored_value`,
            ).toBe(false);
            expect(entry.storedValue, `${expectation.key}: stored_value`).toBeUndefined();
          }
        }
      });
    },
  );
});
