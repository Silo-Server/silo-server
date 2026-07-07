import type { PolicyVendorModule } from "@/api/types";

/**
 * The baseline ships as Rego modules; this module turns them into something an
 * admin can read. Rule summaries live in policyPresentation; the rating and
 * quality ladders are parsed out of the lib module sources so the page always
 * shows the tiers the server actually enforces.
 */

export interface LadderTier {
  rank: number;
  labels: string[];
}

/**
 * Extracts the `rank := { "G": 0, ... }` table from a lib module source and
 * groups entries into ordered tiers. Returns undefined when no table is found
 * (e.g. a future refactor moves it) so callers can fall back to raw source.
 */
export function parseRankLadder(source: string): LadderTier[] | undefined {
  const table = source.match(/rank\s*:?=\s*\{([\s\S]*?)\}/)?.[1];
  if (table === undefined) return undefined;

  const tiers = new Map<number, string[]>();
  const entryPattern = /"([^"]*)"\s*:\s*(\d+)/g;
  for (let entry = entryPattern.exec(table); entry; entry = entryPattern.exec(table)) {
    const [, rawLabel, rawRank] = entry;
    if (rawLabel === undefined || rawRank === undefined) continue;
    const label = rawLabel === "" ? "Any" : rawLabel;
    const rank = Number.parseInt(rawRank, 10);
    const labels = tiers.get(rank) ?? [];
    labels.push(label);
    tiers.set(rank, labels);
  }
  if (tiers.size === 0) return undefined;

  return Array.from(tiers.entries())
    .sort(([a], [b]) => a - b)
    .map(([rank, labels]) => ({ rank, labels }));
}

export interface GroupedVendorModules {
  /** Domain modules keyed by domain name (scope/permission/action). */
  domains: Map<string, PolicyVendorModule>;
  ratings?: PolicyVendorModule;
  quality?: PolicyVendorModule;
  /** Anything this UI doesn't recognize — rendered as source only. */
  other: PolicyVendorModule[];
}

export function groupVendorModules(modules: readonly PolicyVendorModule[]): GroupedVendorModules {
  const grouped: GroupedVendorModules = { domains: new Map(), other: [] };
  for (const module of modules) {
    const base = module.path.split("/").pop() ?? module.path;
    if (base === "ratings.rego") {
      grouped.ratings = module;
    } else if (base === "quality.rego") {
      grouped.quality = module;
    } else {
      const domain = base.replace(/\.rego$/, "");
      if (domain === "scope" || domain === "permission" || domain === "action") {
        grouped.domains.set(domain, module);
      } else {
        grouped.other.push(module);
      }
    }
  }
  return grouped;
}
