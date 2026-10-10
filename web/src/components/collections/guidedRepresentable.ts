import {
  normalizeQueryDefinition,
  type QueryDefinition,
  type QueryDefinitionInput,
} from "@/api/types";

import {
  guidedStateToQueryDefinition,
  queryDefinitionToGuidedState,
} from "./CollectionGuidedRulesEditor";

export const GUIDED_UNAVAILABLE_MESSAGE = "These rules use options the Guided view can't show.";

/**
 * Reports whether the Guided editor can edit qd without losing anything. Guided
 * rebuilds the whole definition from its form state, so a definition qualifies
 * only when that rebuild reproduces it: no OR groups, negated rules, repeated
 * person rules or fields Guided has no control for. Rule and group order are
 * ignored because they don't change which items match.
 */
export function isGuidedRepresentable(qd: QueryDefinition | QueryDefinitionInput): boolean {
  const normalized = normalizeQueryDefinition(qd);
  const rebuilt = guidedStateToQueryDefinition(
    queryDefinitionToGuidedState(normalized),
    normalized,
  );
  return comparisonKey(rebuilt) === comparisonKey(normalized);
}

function comparisonKey(qd: QueryDefinition): string {
  const groups = qd.groups
    .map((group) => {
      const rules = group.rules
        .map(({ field, op, value }) =>
          JSON.stringify([
            field,
            // Genre is an array field, where the server runs "contains" and "is"
            // as the same query, so Guided's switch to "is" changes nothing.
            field === "genre" && op === "contains" ? "is" : op,
            Array.isArray(value) ? value.map(numberAsString) : numberAsString(value),
          ]),
        )
        .sort();
      return JSON.stringify([group.match, rules]);
    })
    .sort();
  return JSON.stringify({ ...qd, groups });
}

// The catalog URL reads "7.5" back as a string and "1917" as a number, while
// Guided writes ratings as numbers and genres as strings. Comparing numbers as
// text keeps that type change from counting as a lost rule.
function numberAsString<T>(value: T): T | string {
  return typeof value === "number" ? String(value) : value;
}
