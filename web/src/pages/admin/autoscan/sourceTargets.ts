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
   * True when the source has no path information at all, so nothing it reports
   * could ever resolve to a library. This is the silent-failure case: the
   * source looks configured and runs cleanly, but can never match.
   */
  unresolvable: boolean;
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
 * Match a source's resolved paths against library roots.
 *
 * A source that emits native paths but has neither rewrites nor path config is
 * still reported as unresolvable — it has genuinely told us nothing about where
 * its media lands.
 */
export function sourceTargets(
  source: AutoscanSource,
  descriptor: AutoscanScanSourceDescriptor,
  libraries: readonly Library[],
): SourceTargets {
  const paths = resolvedPathsFor(source, descriptor);
  if (paths.length === 0) {
    return { libraries: [], unresolvable: true };
  }

  const matched = libraries.filter((library) =>
    (library.paths ?? []).some((root) => {
      const normalizedRoot = normalizePath(root);
      return paths.some((path) => isWithin(path, normalizedRoot));
    }),
  );

  // Paths exist but match no library root. Not "unresolvable" in the sense
  // above — the operator has said something, it just doesn't line up — so the
  // caller renders a different, more specific warning.
  return { libraries: matched, unresolvable: false };
}

/** Short human summary of what a source keeps fresh. */
export function describeTargets(targets: SourceTargets): string {
  if (targets.unresolvable) return "No paths configured";
  if (targets.libraries.length === 0) return "No matching library";
  return targets.libraries.map((library) => library.name).join(", ");
}
