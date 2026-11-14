import type {
  MarkerKind,
  MarkerRegionView,
  PlayerFileVersion,
  PlayerMarkerProvenance,
  PlayerMarkerRange,
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

/**
 * Ranges and the provenance recorded beside them, as a version holds them.
 *
 * Wider than [rangesEqual] on purpose: an update can change only where a range
 * came from, and a comparison that ignored that would report the version
 * unchanged and drop the answer, leaving the skip prompt's source line empty.
 */
function markersEqual(
  a: PlayerMarkerRange | null | undefined,
  b: PlayerMarkerRange | null | undefined,
): boolean {
  return (
    rangesEqual(a, b) &&
    (a?.source ?? null) === (b?.source ?? null) &&
    (a?.provider ?? null) === (b?.provider ?? null) &&
    (a?.confidence ?? null) === (b?.confidence ?? null) &&
    (a?.algorithm ?? null) === (b?.algorithm ?? null)
  );
}

function segmentsEqual(a: PlayerMarkerSegment[] | undefined, b: PlayerMarkerSegment[] | undefined) {
  if (a === b) return true;
  if (a === undefined || b === undefined || a.length !== b.length) return false;
  return a.every(
    (segment, index) =>
      segment.kind === b[index]?.kind &&
      segment.start_seconds === b[index]?.start_seconds &&
      segment.end_seconds === b[index]?.end_seconds &&
      segment.thumbnail_url === b[index]?.thumbnail_url &&
      segment.thumbnail_thumbhash === b[index]?.thumbnail_thumbhash &&
      segment.thumbnail_capture_seconds === b[index]?.thumbnail_capture_seconds,
  );
}

/**
 * A version's marker after an update reports its range.
 *
 * An update that names a provenance is the whole answer for that range: it is
 * the server describing the marker as its row now holds it, and a confidence
 * left out of it means the range is unrated rather than that the client should
 * keep an older rating.
 *
 * An update that names none is a bare range, which is what a server without the
 * provenance fields sends and what the editor's draft carries. A range it
 * restates keeps the provenance the version already holds — the row's own answer
 * for that kind, which a range that did not move cannot have changed, and which
 * is what a skip prompt reads to draw its source line. A range that moved takes
 * nothing from the range it replaced: a moved range was detected or supplied by
 * someone the update does not name, and a stale attribution answers the
 * question the line exists to answer with something false.
 */
function rangeAfterUpdate(
  existing: PlayerMarkerRange | null | undefined,
  update: PlayerMarkerRange | null,
): PlayerMarkerRange | null {
  if (update === null) return null;
  if (namesAProvenance(update)) return update;
  const restated =
    existing != null && existing.start === update.start && existing.end === update.end;
  return restated ? { ...update, ...provenanceOf(existing) } : update;
}

/** Whether a range says anything about where it came from. */
function namesAProvenance(range: PlayerMarkerProvenance): boolean {
  return (
    Boolean(range.source) ||
    Boolean(range.provider) ||
    Boolean(range.algorithm) ||
    range.confidence != null
  );
}

export function patchVersionMarkers(
  versions: PlayerFileVersion[],
  fileId: number,
  intro?: PlayerMarkerRange | null,
  credits?: PlayerMarkerRange | null,
  recap?: PlayerMarkerRange | null,
  preview?: PlayerMarkerRange | null,
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

    const nextIntro = intro === undefined ? version.intro : rangeAfterUpdate(version.intro, intro);
    const nextCredits =
      credits === undefined ? version.credits : rangeAfterUpdate(version.credits, credits);
    const nextRecap = recap === undefined ? version.recap : rangeAfterUpdate(version.recap, recap);
    const nextPreview =
      preview === undefined ? version.preview : rangeAfterUpdate(version.preview, preview);
    let nextSegments =
      markerSegments?.map((segment) => {
        const previous = version.marker_segments?.find(
          (candidate) =>
            candidate.kind === segment.kind &&
            candidate.start_seconds === segment.start_seconds &&
            candidate.end_seconds === segment.end_seconds,
        );
        // Range updates contain navigation metadata only. Keep a ready image for
        // an unchanged occurrence when another provider occurrence changes.
        return !segment.thumbnail_url && previous?.thumbnail_url
          ? {
              ...segment,
              thumbnail_url: previous.thumbnail_url,
              thumbnail_thumbhash: previous.thumbnail_thumbhash,
              thumbnail_capture_seconds: previous.thumbnail_capture_seconds,
            }
          : segment;
      }) ?? version.marker_segments;
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
      markersEqual(version.intro, nextIntro) &&
      markersEqual(version.credits, nextCredits) &&
      markersEqual(version.recap, nextRecap) &&
      markersEqual(version.preview, nextPreview) &&
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

/**
 * The provenance fields a region carries, taken from the singular per-kind
 * marker. Occurrences in `marker_segments` do not carry their own: the file row
 * stores one origin per kind, so every occurrence of a kind shares it, and
 * inventing a per-occurrence answer would be a guess.
 *
 * Absent stays absent — an unrated marker's confidence is 0 in the row, and
 * spreading that as `confidence: 0` would render a real "0%" for a range nobody
 * rated.
 */
function provenanceOf(range: PlayerMarkerRange | null | undefined) {
  return {
    ...(range?.source ? { source: range.source } : {}),
    ...(range?.provider ? { provider: range.provider } : {}),
    ...(range?.confidence != null ? { confidence: range.confidence } : {}),
    ...(range?.algorithm ? { algorithm: range.algorithm } : {}),
  };
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
      ...provenanceOf(version[segment.kind]),
      ...(segment.thumbnail_url
        ? {
            thumbnail_url: segment.thumbnail_url,
            thumbnail_thumbhash: segment.thumbnail_thumbhash,
            thumbnail_capture_seconds: segment.thumbnail_capture_seconds,
          }
        : {}),
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
  intro: PlayerMarkerRange | null;
  credits: PlayerMarkerRange | null;
  recap: PlayerMarkerRange | null;
  preview: PlayerMarkerRange | null;
} {
  return {
    intro: version?.intro ?? null,
    credits: version?.credits ?? null,
    recap: version?.recap ?? null,
    preview: version?.preview ?? null,
  };
}
