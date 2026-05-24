import type { PlayerFileVersion, PlayerTimeRange } from "../types";

// rangesEqual treats undefined and null as equivalent (both mean "absent")
// because the markers_updated event nulls out absent segments while the
// initial version state may have them undefined.
function rangesEqual(a: PlayerTimeRange | null | undefined, b: PlayerTimeRange | null | undefined) {
  return (a?.start ?? null) === (b?.start ?? null) && (a?.end ?? null) === (b?.end ?? null);
}

export function patchVersionMarkers(
  versions: PlayerFileVersion[],
  fileId: number,
  intro?: PlayerTimeRange | null,
  credits?: PlayerTimeRange | null,
  recap?: PlayerTimeRange | null,
  preview?: PlayerTimeRange | null,
): PlayerFileVersion[] {
  if (
    intro === undefined &&
    credits === undefined &&
    recap === undefined &&
    preview === undefined
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
    if (
      rangesEqual(version.intro, nextIntro) &&
      rangesEqual(version.credits, nextCredits) &&
      rangesEqual(version.recap, nextRecap) &&
      rangesEqual(version.preview, nextPreview)
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
    };
  });

  return changed ? nextVersions : versions;
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
