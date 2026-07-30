import type { AutoscanScanSourceDescriptor, AutoscanSource, Library } from "@/api/types";

import { configFields } from "./sourceDescriptor";

/**
 * Which libraries a scan source can actually feed, and whether it can feed
 * anything at all.
 *
 * A source never states its target directly. What it has is a set of paths —
 * either the `to` side of its path rewrites (for sources whose provider speaks a
 * different namespace) or its own path-shaped config values (for local
 * watchers). Matching those against library roots is what turns "a source
 * exists" into "this keeps TV Shows fresh", which is the single thing the old
 * UI never said.
 */
export interface SourceTargets {
  /** Libraries whose roots overlap this source's resolved paths. */
  libraries: Library[];
  /**
   * True when the source was asked for paths and given none, so nothing it
   * reports could ever resolve to a library. This is the silent-failure case: a
   * local watcher that looks configured and runs cleanly, but can never match.
   * Reserved for sources that own their paths — see targetsAreKnowable.
   */
  unresolvable: boolean;
  /**
   * True when the source's targets are not knowable up front: it learns its
   * paths at runtime (from a provider it polls, from a webhook payload, or
   * because it declares emits_native_paths) and has no rewrites narrowing them
   * down. The UI must say "unknown" rather than warn that it is broken.
   */
  unknown: boolean;
}

/** Normalize a path for prefix comparison: trimmed, no trailing slash. */
function normalizePath(path: string): string {
  const trimmed = path.trim().replace(/\/+$/, "");
  return trimmed;
}

/** Whether `candidate` is at or below `root`. */
function isWithin(candidate: string, root: string): boolean {
  if (!candidate || !root) return false;
  if (candidate === root) return true;
  return candidate.startsWith(`${root}/`) || root.startsWith(`${candidate}/`);
}

/**
 * Config values that look like filesystem paths. Descriptor fields carry no
 * "this is a path" flag, so absolute-looking lines are treated as paths — the
 * cost of a false positive here is only an extra library chip, never a wrong
 * scan.
 */
function pathsFromConfig(
  source: AutoscanSource,
  descriptor: AutoscanScanSourceDescriptor,
): string[] {
  const keys = new Set(configFields(descriptor).map((field) => field.key));
  const out: string[] = [];

  for (const [key, value] of Object.entries(source.source_config ?? {})) {
    // Restrict to declared fields when the descriptor has any, so unrelated
    // scalar config (tokens, provider names) is never read as a path.
    if (keys.size > 0 && !keys.has(key)) continue;
    for (const line of String(value ?? "").split(/\r?\n/)) {
      const path = normalizePath(line);
      if (path.startsWith("/")) out.push(path);
    }
  }
  return out;
}

/**
 * Every Silo-native path this source can produce: rewrite targets first (they
 * are authoritative when present), else its own path-shaped config.
 */
export function resolvedPathsFor(
  source: AutoscanSource,
  descriptor: AutoscanScanSourceDescriptor,
): string[] {
  const rewriteTargets = (source.path_rewrites ?? [])
    .map((rewrite) => normalizePath(rewrite.to))
    .filter(Boolean);

  if (rewriteTargets.length > 0) return rewriteTargets;
  return pathsFromConfig(source, descriptor);
}

/**
 * Whether a source's targets could have been known from its stored
 * configuration alone — which is what makes an empty result a misconfiguration
 * rather than simply unknown.
 *
 * Three kinds of source learn their paths at runtime instead, so empty rewrites
 * are a valid passthrough for them, not a silent failure:
 *
 * - a webhook source reads paths out of each delivery payload;
 * - a source with a bound connection gets them from the provider's root folders
 *   (`SuggestRewrites` deliberately proposes nothing when a reported root
 *   already equals the Silo path, so "correctly configured" and "no rewrites"
 *   are the same state);
 * - an `emits_native_paths` source returns Silo paths directly.
 *
 * What remains is a source that must be handed its roots up front. It is only
 * worth warning about when the descriptor gives the operator a field to put
 * them in — otherwise the warning points at a control that does not exist.
 */
function targetsAreKnowable(
  source: AutoscanSource,
  descriptor: AutoscanScanSourceDescriptor,
): boolean {
  if (source.delivery_mode === "webhook") return false;
  if (descriptor.emits_native_paths) return false;
  // The bound connection is what matters, not just the descriptor's
  // requirement: an `optional` source with nothing bound is a local watcher for
  // this purpose, and must still be warned about when its path fields are
  // empty. A `required` source with nothing bound is excluded because the row
  // already reports the missing connection, and that is the fault to fix first.
  if (source.connection_id || descriptor.connection === "required") return false;
  return configFields(descriptor).length > 0;
}

/**
 * Match a source's resolved paths against library roots.
 *
 * A source with no paths is only reported as unresolvable when it is one whose
 * paths could have been known up front; otherwise its targets are unknown.
 */
export function sourceTargets(
  source: AutoscanSource,
  descriptor: AutoscanScanSourceDescriptor,
  libraries: readonly Library[],
): SourceTargets {
  const paths = resolvedPathsFor(source, descriptor);
  if (paths.length === 0) {
    if (!targetsAreKnowable(source, descriptor)) {
      return { libraries: [], unresolvable: false, unknown: true };
    }
    return { libraries: [], unresolvable: true, unknown: false };
  }

  // Disabled libraries are skipped by the scanner, so counting one as a target
  // would present a source as correctly wired while every delivery it resolves
  // there is dropped.
  const matched = libraries.filter(
    (library) =>
      library.enabled &&
      (library.paths ?? []).some((root) => {
        const normalizedRoot = normalizePath(root);
        return paths.some((path) => isWithin(path, normalizedRoot));
      }),
  );

  // Paths exist but match no library root. Not "unresolvable" in the sense
  // above — the operator has said something, it just doesn't line up — so the
  // caller renders a different, more specific warning.
  return { libraries: matched, unresolvable: false, unknown: false };
}

/** Short human summary of what a source keeps fresh. */
export function describeTargets(targets: SourceTargets): string {
  if (targets.unresolvable) return "No paths configured";
  if (targets.unknown) return "Determined at scan time";
  if (targets.libraries.length === 0) return "No matching library";
  return targets.libraries.map((library) => library.name).join(", ");
}
