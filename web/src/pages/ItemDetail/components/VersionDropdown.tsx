import { memo, useMemo, useState } from "react";
import { Check, ChevronDown, Disc3, Layers3 } from "lucide-react";

import type { FileVersion, PlaybackVariant } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { videoRangeLabel } from "@/lib/videoRange";
import { useT, type Translate } from "@/i18n";
import DetailPopover from "./DetailPopover";
import { playbackEditions } from "./versionRankingUtils";
import { buildDetailLine, buildQualitySummary, sortByResolution } from "./VersionFlyout";

interface VersionDropdownProps {
  versions: FileVersion[];
  playbackVariants?: PlaybackVariant[];
  selectedVersion: FileVersion | null;
  onSelectVersion: (version: FileVersion | null) => void;
  onSelectEdition?: (key: string) => void;
  versionPicked?: boolean;
}

interface EditionOption {
  id: string;
  label: string;
  defaultVersion: FileVersion;
  versions: FileVersion[];
  allVersions: FileVersion[];
}

function VersionDropdown({
  versions,
  playbackVariants,
  selectedVersion,
  onSelectVersion,
  onSelectEdition,
  versionPicked = false,
}: VersionDropdownProps) {
  const t = useT();
  const [editionOpen, setEditionOpen] = useState(false);
  const [versionOpen, setVersionOpen] = useState(false);

  const sorted = useMemo(() => sortByResolution(versions), [versions]);
  const editionOptions = useMemo(
    () => buildEditionOptions(t, playbackVariants, versions),
    [t, playbackVariants, versions],
  );

  const showEditionDropdown = editionOptions.length > 1;

  const selectedEdition = resolveSelectedEditionOption(editionOptions, selectedVersion);
  const activeVersions = selectedEdition?.versions ?? sorted;
  const activeVersion =
    selectedVersion ?? selectedEdition?.defaultVersion ?? activeVersions[0] ?? null;
  const showVersionDropdown = activeVersions.length > 1;

  if (!showEditionDropdown && !showVersionDropdown) {
    return null;
  }

  return (
    <>
      {showEditionDropdown && selectedEdition ? (
        <DetailPopover
          open={editionOpen}
          onOpenChange={setEditionOpen}
          contentClassName="w-72 p-1.5"
          trigger={
            <Button
              variant="glass"
              className="h-8 max-w-full min-w-0 shrink gap-1.5 rounded-full px-3 text-xs font-medium"
            >
              <Layers3 className="size-3.5" />
              {t("web.detail.versions.editionButton")}
              <span className="text-muted-foreground max-w-44 truncate text-[0.6875rem] font-normal sm:max-w-64">
                {selectedEdition.label}
              </span>
              <ChevronDown className="text-muted-foreground size-3" />
            </Button>
          }
        >
          <div className="space-y-0.5">
            {editionOptions.map((option) => {
              const isSelected = option.id === selectedEdition.id;
              const detail = buildEditionDetail(t, option);

              return (
                <button
                  key={option.id}
                  type="button"
                  onClick={() => {
                    onSelectEdition?.(option.id);
                    setEditionOpen(false);
                    setVersionOpen(false);
                  }}
                  className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-start transition-colors ${
                    isSelected ? "bg-accent text-accent-foreground" : "hover:bg-accent/50"
                  }`}
                >
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{option.label}</div>
                    {detail && (
                      <div className="text-muted-foreground truncate text-xs">{detail}</div>
                    )}
                  </div>
                  {isSelected && <Check className="text-primary size-4 shrink-0" />}
                </button>
              );
            })}
          </div>
        </DetailPopover>
      ) : null}

      {showVersionDropdown ? (
        <DetailPopover
          open={versionOpen}
          onOpenChange={setVersionOpen}
          contentClassName="w-80 p-1.5"
          trigger={
            <Button
              variant="glass"
              className="h-8 max-w-full min-w-0 shrink gap-1.5 rounded-full px-3 text-xs font-medium"
            >
              <Disc3 className="size-3.5" />
              {t("web.detail.versions.versionButton")}
              <span className="text-muted-foreground max-w-44 truncate text-[0.6875rem] font-normal sm:max-w-64">
                {activeVersion ? buildVersionTriggerSummary(t, activeVersion, versionPicked) : ""}
              </span>
              <ChevronDown className="text-muted-foreground size-3" />
            </Button>
          }
        >
          <div className="space-y-0.5">
            <button
              type="button"
              onClick={() => {
                onSelectVersion(null);
                setVersionOpen(false);
              }}
              className={`flex w-full items-center justify-between rounded-lg px-3 py-2.5 text-sm ${!versionPicked ? "bg-accent text-accent-foreground" : "hover:bg-accent/50"}`}
            >
              {t("web.player.quality.auto")}
              {!versionPicked && <Check className="text-primary size-4 shrink-0" />}
            </button>
            {activeVersions.map((version) => {
              const isSelected = versionPicked && version.file_id === activeVersion?.file_id;
              const summary = buildQualitySummary(version);
              const detail = buildDetailLine(version);
              const rangeLabel = videoRangeLabel(version);

              return (
                <button
                  key={version.file_id}
                  type="button"
                  onClick={() => {
                    onSelectVersion(version);
                    setVersionOpen(false);
                  }}
                  className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-start transition-colors ${
                    isSelected ? "bg-accent text-accent-foreground" : "hover:bg-accent/50"
                  }`}
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium">
                        {summary ||
                          t("web.detail.versions.fileIdFallback", { id: version.file_id })}
                      </span>
                      {rangeLabel ? (
                        <Badge
                          variant="secondary"
                          className="px-1.5 py-0 text-[0.625rem] uppercase"
                        >
                          {rangeLabel}
                        </Badge>
                      ) : null}
                    </div>
                    {detail && (
                      <span className="text-muted-foreground block truncate text-xs">{detail}</span>
                    )}
                  </div>
                  {isSelected && <Check className="text-primary size-4 shrink-0" />}
                </button>
              );
            })}
          </div>
        </DetailPopover>
      ) : null}
    </>
  );
}

export default memo(VersionDropdown);

function buildVersionTriggerSummary(t: Translate, version: FileVersion, picked: boolean): string {
  const summary =
    buildQualitySummary(version) ||
    buildDetailLine(version) ||
    t("web.detail.versions.fileIdFallback", { id: version.file_id });
  return picked ? summary : [t("web.player.quality.auto"), summary].join(" · ");
}

function buildEditionOptions(
  t: Translate,
  playbackVariants: PlaybackVariant[] | undefined,
  versions: FileVersion[],
): EditionOption[] {
  const editions = playbackEditions(versions, playbackVariants);
  const hasNamedEditions = editions.some((edition) => edition.key);
  return editions.flatMap((edition) => {
    const sorted = sortByResolution(edition.versions);
    const defaultVersion = sorted[0];
    if (!defaultVersion) return [];
    return [
      {
        id: edition.key,
        label:
          edition.raw?.trim() ||
          (edition.key
            ? humanizeEditionKey(edition.key)
            : t(
                hasNamedEditions
                  ? "web.detail.versions.standardEdition"
                  : "web.detail.versions.editionFallback",
              )),
        defaultVersion,
        versions: sorted,
        allVersions: edition.allVersions,
      },
    ];
  });
}

function resolveSelectedEditionOption(
  editionOptions: EditionOption[],
  selectedVersion: FileVersion | null,
): EditionOption | null {
  return (
    editionOptions.find((option) =>
      option.allVersions.some((v) => v.file_id === selectedVersion?.file_id),
    ) ??
    editionOptions[0] ??
    null
  );
}

function humanizeEditionKey(value: string): string {
  return value
    .split(/[_-]+/)
    .filter(Boolean)
    .map((part) => {
      if (part.toLowerCase() === "imax") {
        return "IMAX";
      }
      return `${part.charAt(0).toUpperCase()}${part.slice(1)}`;
    })
    .join(" ");
}

function buildEditionDetail(t: Translate, option: EditionOption): string {
  const parts: string[] = [];
  if (option.versions.length > 1) {
    parts.push(
      t("web.detail.versions.versionCount", {
        count: option.versions.length,
        count_text: option.versions.length,
      }),
    );
  }

  const defaultSummary = buildQualitySummary(option.defaultVersion);
  if (defaultSummary) {
    parts.push(defaultSummary);
  }

  return parts.join(" · ");
}
