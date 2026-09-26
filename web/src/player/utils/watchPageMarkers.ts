import type {
  MarkerKind,
  MarkerRegionView,
  PlayerFileVersion,
  PlayerMarkerSegment,
  PlayerTimeRange,
} from "../types";

const markerKinds: MarkerKind[] = ["intro", "credits", "recap", "preview"];

// rangesEqual treats undefined and null as equivalent (both mean "absent")
// because the markers_updated event nulls out absent segments while the
// initial version state may have them undefined.
function rangesEqual(a: PlayerTimeRange | null | undefined, b: PlayerTimeRange | null | undefined) {
  return (a?.start ?? null) === (b?.start ?? null) && (a?.end ?? null) === (b?.end ?? null);
}

function segmentsEqual(a: PlayerMarkerSegment[] | undefined, b: PlayerMarkerSegment[] | undefined) {
  if (a === b) return true;
  if (a === undefined || b === undefined || a.length !== b.length) return false;
  return a.every(
    (segment, index) =>
      segment.kind === b[index]?.kind &&
      segment.start_seconds === b[index]?.start_seconds &&
      segment.end_seconds === b[index]?.end_seconds,
  );
}

export function patchVersionMarkers(
  versions: PlayerFileVersion[],
  fileId: number,
  intro?: PlayerTimeRange | null,
  credits?: PlayerTimeRange | null,
  recap?: PlayerTimeRange | null,
  preview?: PlayerTimeRange | null,
  markerSegments?: PlayerMarkerSegment[],
): PlayerFileVersion[] {
  if (
    intro === undefined &&
    credits === undefined &&
    recap === undefined &&
    preview === undefined &&
    markerSegments === undefined
  ) {
    return versions;
  }

  let changed = false;
  const nextVersions = versions.map((version) => {
    if (version.file_id !== fileId) {
      return version;
    }

    const nextIntro = intro === undefined ? version.intro : intro;
    const nextCredits = credits === undefined ? version.credits : credits;
    const nextRecap = recap === undefined ? version.recap : recap;
    const nextPreview = preview === undefined ? version.preview : preview;
    let nextSegments = markerSegments ?? version.marker_segments;
    if (markerSegments === undefined && nextSegments !== undefined) {
      const updates = { intro, credits, recap, preview };
      for (const kind of markerKinds) {
        const range = updates[kind];
        // The editor returns its full draft; preserve additional occurrences
        // for kinds the viewer did not change.
        if (range === undefined || rangesEqual(range, version[kind])) continue;
        nextSegments = nextSegments.filter((segment) => segment.kind !== kind);
        if (range) {
          nextSegments.push({ kind, start_seconds: range.start, end_seconds: range.end });
        }
      }
    }
    if (
      rangesEqual(version.intro, nextIntro) &&
      rangesEqual(version.credits, nextCredits) &&
      rangesEqual(version.recap, nextRecap) &&
      rangesEqual(version.preview, nextPreview) &&
      segmentsEqual(version.marker_segments, nextSegments)
    ) {
      return version;
    }

    changed = true;
    return {
      ...version,
      intro: nextIntro,
      credits: nextCredits,
      recap: nextRecap,
      preview: nextPreview,
      marker_segments: nextSegments,
    };
  });

  return changed ? nextVersions : versions;
}

/** Prefer the complete inventory, retaining singular fields for older servers. */
export function resolveMarkerRegions(
  version: Pick<PlayerFileVersion, "intro" | "credits" | "recap" | "preview" | "marker_segments">,
): MarkerRegionView[] {
  const regions =
    version.marker_segments?.map((segment) => ({
      kind: segment.kind,
      start: segment.start_seconds,
      end: segment.end_seconds,
    })) ??
    markerKinds.flatMap((kind) => {
      const range = version[kind];
      return range ? [{ kind, ...range }] : [];
    });
  return regions
    .filter(
      (region) =>
        Number.isFinite(region.start) &&
        Number.isFinite(region.end) &&
        region.start >= 0 &&
        region.end > region.start,
    )
    .sort((a, b) => a.start - b.start || a.end - b.end);
}

/** Prefer an active occurrence, retaining the previous one after a skip for undo. */
export function markerOccurrenceAtTime(
  regions: MarkerRegionView[],
  kind: MarkerKind,
  currentTime: number,
): MarkerRegionView | null {
  const occurrences = regions.filter((region) => region.kind === kind);
  // Start empty rather than seeded with occurrences[0]: before the first
  // occurrence begins the answer is "no occurrence", and callers that trusted
  // the returned region without re-checking the playhead would otherwise be
  // handed a range that has not started.
  let occurrence: MarkerRegionView | null = null;
  let active: MarkerRegionView | null = null;
  for (const region of occurrences) {
    if (region.start > currentTime) break;
    if (!occurrence || region.end > occurrence.end) occurrence = region;
    if (currentTime < region.end) active = region;
  }
  if (active) {
    // Equal-end occurrences use the same earliest-start identity before and
    // after a skip so the intro's undo prompt survives the position change.
    const end = active.end;
    return occurrences.find((region) => region.end === end) ?? active;
  }
  return occurrence;
}

/** Only leave the episode once all unmarked scenes have played. */
export function resolveAutoplayMarker(
  regions: MarkerRegionView[],
  duration: number,
  allowPreview: boolean,
): PlayerTimeRange | null {
  if (duration <= 0) return null;
  let start = duration;
  for (let index = regions.length - 1; index >= 0; index -= 1) {
    const region = regions[index]!;
    if (region.kind !== "credits" && !(allowPreview && region.kind === "preview")) continue;
    // Allow timestamp rounding at the end, but never span narrative gaps.
    if (region.end >= start - 1) start = Math.min(start, region.start);
  }
  return start < duration ? { start, end: duration } : null;
}

export function resolveActiveVersionMarkers(
  version: Pick<PlayerFileVersion, "intro" | "credits" | "recap" | "preview"> | null | undefined,
): {
  intro: PlayerTimeRange | null;
  credits: PlayerTimeRange | null;
  recap: PlayerTimeRange | null;
  preview: PlayerTimeRange | null;
} {
  return {
    intro: version?.intro ?? null,
    credits: version?.credits ?? null,
    recap: version?.recap ?? null,
    preview: version?.preview ?? null,
  };
}
