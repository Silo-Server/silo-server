import { useMemo, useState } from "react";
import { Check, ChevronDown, Disc3, Layers3 } from "lucide-react";

import type { FileVersion, PlaybackVariant } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { sortPlaybackVariantsByEditionPreference } from "./versionRankingUtils";
import { buildDetailLine, buildQualitySummary, sortByResolution } from "./VersionFlyout";

interface VersionDropdownProps {
  versions: FileVersion[];
  playbackVariants?: PlaybackVariant[];
  selectedVersion: FileVersion | null;
  onSelectVersion: (version: FileVersion) => void;
}

interface EditionOption {
  id: string;
  label: string;
  variant: PlaybackVariant;
  defaultVersion: FileVersion;
  versions: FileVersion[];
}

export default function VersionDropdown({
  versions,
  playbackVariants,
  selectedVersion,
  onSelectVersion,
}: VersionDropdownProps) {
  const [editionOpen, setEditionOpen] = useState(false);
  const [versionOpen, setVersionOpen] = useState(false);

  const sorted = useMemo(() => sortByResolution(versions), [versions]);
  const editionOptions = useMemo(
    () => buildEditionOptions(playbackVariants, versions),
    [playbackVariants, versions],
  );

  const hasNamedEditions = editionOptions.some((option) => option.variant.edition_key);
  const showEditionDropdown =
    editionOptions.length > 1 &&
    new Set(editionOptions.map((option) => option.label.toLowerCase())).size > 1 &&
    hasNamedEditions;

  const selectedEdition = showEditionDropdown
    ? resolveSelectedEditionOption(editionOptions, selectedVersion)
    : null;
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
        <Popover open={editionOpen} onOpenChange={setEditionOpen}>
          <PopoverTrigger asChild>
            <Button
              variant="glass"
              className="h-8 max-w-full min-w-0 shrink gap-1.5 rounded-full px-3 text-xs font-medium"
            >
              <Layers3 className="size-3.5" />
              Edition
              <span className="text-muted-foreground max-w-44 truncate text-[11px] font-normal sm:max-w-64">
                {selectedEdition.label}
              </span>
              <ChevronDown className="text-muted-foreground size-3" />
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-72 p-1.5">
            <div className="space-y-0.5">
              {editionOptions.map((option) => {
                const isSelected = option.id === selectedEdition.id;
                const detail = buildEditionDetail(option);

                return (
                  <button
                    key={option.id}
                    type="button"
                    onClick={() => {
                      onSelectVersion(option.defaultVersion);
                      setEditionOpen(false);
                      setVersionOpen(false);
                    }}
                    className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left transition-colors ${
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
          </PopoverContent>
        </Popover>
      ) : null}

      {showVersionDropdown ? (
        <Popover open={versionOpen} onOpenChange={setVersionOpen}>
          <PopoverTrigger asChild>
            <Button
              variant="glass"
              className="h-8 max-w-full min-w-0 shrink gap-1.5 rounded-full px-3 text-xs font-medium"
            >
              <Disc3 className="size-3.5" />
              Version
              <span className="text-muted-foreground max-w-44 truncate text-[11px] font-normal sm:max-w-64">
                {activeVersion ? buildVersionTriggerSummary(activeVersion) : ""}
              </span>
              <ChevronDown className="text-muted-foreground size-3" />
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-80 p-1.5">
            <div className="space-y-0.5">
              {activeVersions.map((version) => {
                const isSelected = version.file_id === activeVersion?.file_id;
                const summary = buildQualitySummary(version);
                const detail = buildDetailLine(version);

                return (
                  <button
                    key={version.file_id}
                    type="button"
                    onClick={() => {
                      onSelectVersion(version);
                      setVersionOpen(false);
                    }}
                    className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left transition-colors ${
                      isSelected ? "bg-accent text-accent-foreground" : "hover:bg-accent/50"
                    }`}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate text-sm font-medium">
                          {summary || `Version ${version.file_id}`}
                        </span>
                        {version.hdr ? (
                          <Badge variant="secondary" className="px-1.5 py-0 text-[10px] uppercase">
                            HDR
                          </Badge>
                        ) : null}
                      </div>
                      {detail && (
                        <span className="text-muted-foreground block truncate text-xs">
                          {detail}
                        </span>
                      )}
                    </div>
                    {isSelected && <Check className="text-primary size-4 shrink-0" />}
                  </button>
                );
              })}
            </div>
          </PopoverContent>
        </Popover>
      ) : null}
    </>
  );
}

function buildVersionTriggerSummary(version: FileVersion): string {
  return buildQualitySummary(version) || buildDetailLine(version) || `Version ${version.file_id}`;
}

function buildEditionOptions(
  playbackVariants: PlaybackVariant[] | undefined,
  versions: FileVersion[],
): EditionOption[] {
  if (!playbackVariants || playbackVariants.length === 0) {
    return [];
  }

  const orderedVariants = sortPlaybackVariantsByEditionPreference(playbackVariants);
  const hasNamedEditions = orderedVariants.some((variant) => variant.edition_key);

  return orderedVariants
    .map((variant) => {
      const firstPart = [...(variant.parts ?? [])].sort((a, b) => a.part_index - b.part_index)[0];
      if (!firstPart) {
        return null;
      }

      const partVersions = sortByResolution([...(firstPart.versions ?? [])]);
      const defaultVersion =
        (firstPart.default_file_id != null
          ? versions.find((version) => version.file_id === firstPart.default_file_id)
          : undefined) ?? partVersions[0];
      if (!defaultVersion) {
        return null;
      }

      return {
        id: variant.variant_id,
        label: buildEditionLabel(variant, hasNamedEditions),
        variant,
        defaultVersion,
        versions: partVersions,
      };
    })
    .filter((entry): entry is EditionOption => !!entry);
}

function resolveSelectedEditionOption(
  editionOptions: EditionOption[],
  selectedVersion: FileVersion | null,
): EditionOption | null {
  if (editionOptions.length === 0) {
    return null;
  }

  if (selectedVersion) {
    const matching = editionOptions.find((option) =>
      option.variant.parts.some((part) =>
        part.versions.some((candidate) => candidate.file_id === selectedVersion.file_id),
      ),
    );
    if (matching) {
      return matching;
    }
  }

  return editionOptions[0] ?? null;
}

function buildEditionLabel(variant: PlaybackVariant, hasNamedEditions: boolean): string {
  if (variant.edition_raw?.trim()) {
    return variant.edition_raw.trim();
  }
  if (variant.edition_key?.trim()) {
    return humanizeEditionKey(variant.edition_key);
  }
  return hasNamedEditions ? "Standard" : "Edition";
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

function buildEditionDetail(option: EditionOption): string {
  const parts: string[] = [];
  if (option.versions.length > 1) {
    parts.push(`${option.versions.length} versions`);
  }

  const defaultSummary = buildQualitySummary(option.defaultVersion);
  if (defaultSummary) {
    parts.push(defaultSummary);
  }

  return parts.join(" · ");
}
