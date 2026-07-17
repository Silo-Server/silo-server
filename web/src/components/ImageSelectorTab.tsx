import { useState, useMemo, useCallback } from "react";
import { Check, Loader2, AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import type { ItemDetail, RemoteImage } from "@/api/types";
import { useItemImages, useApplyItemImage } from "@/hooks/queries/items";
import { cn } from "@/lib/utils";

type ImageTab = "poster" | "backdrop" | "logo";

const IMAGE_TABS: { key: ImageTab; label: string; aspect: string }[] = [
  { key: "poster", label: "Posters", aspect: "aspect-[2/3]" },
  { key: "backdrop", label: "Backdrops", aspect: "aspect-video" },
  { key: "logo", label: "Logos", aspect: "aspect-video" },
];

// Seasons only store a poster; the server rejects other image types for them.
const SEASON_TABS = IMAGE_TABS.filter((tab) => tab.key === "poster");

interface ImageSelectorTabProps {
  item: ItemDetail;
  enabled: boolean;
}

export default function ImageSelectorTab({ item, enabled }: ImageSelectorTabProps) {
  const availableTabs = item.type === "season" ? SEASON_TABS : IMAGE_TABS;
  const [activeTab, setActiveTab] = useState<ImageTab>("poster");
  const [textlessOnly, setTextlessOnly] = useState(false);
  const [selectedImage, setSelectedImage] = useState<RemoteImage | null>(null);
  // Track which images were applied in this session (original_url per type).
  const [appliedImages, setAppliedImages] = useState<Record<string, string>>({});

  const { data, isLoading, isError } = useItemImages(item.content_id, enabled);
  const applyMutation = useApplyItemImage();

  const images = data?.images ?? [];
  const current = data?.current;
  const providerErrors = data?.provider_errors;

  const tabConfig = IMAGE_TABS.find((t) => t.key === activeTab)!;

  const filteredImages = useMemo(() => {
    let result = images.filter((img) => img.type === activeTab);
    if (textlessOnly && activeTab !== "logo") {
      result = result.filter((img) => img.language === "");
    }
    return result;
  }, [images, activeTab, textlessOnly]);

  // The "current" image for this tab. We first check session-local applied
  // images (which use original_url), then fall back to the server's stored
  // path (which may be an S3 key or a plugin path depending on cache state).
  const currentOriginalUrl = appliedImages[activeTab] ?? "";
  const currentStoredPath = useMemo(() => {
    if (!current) return "";
    switch (activeTab) {
      case "poster":
        return current.poster_url ?? "";
      case "backdrop":
        return current.backdrop_url ?? "";
      case "logo":
        return current.logo_url ?? "";
    }
  }, [current, activeTab]);

  const handleApply = useCallback(() => {
    if (!selectedImage) return;
    applyMutation.mutate(
      {
        item,
        request: {
          original_url: selectedImage.original_url,
          type: selectedImage.type,
          provider_id: selectedImage.provider_id,
        },
      },
      {
        onSuccess: () => {
          setAppliedImages((prev) => ({
            ...prev,
            [selectedImage.type]: selectedImage.original_url,
          }));
          setSelectedImage(null);
        },
      },
    );
  }, [selectedImage, applyMutation, item]);

  return (
    <div className="flex h-full flex-col gap-3">
      {/* Info banner */}
      <div className="bg-muted/50 text-muted-foreground shrink-0 rounded-lg px-3 py-2 text-[11px]">
        Image changes apply immediately and are not affected by Cancel.
      </div>

      {/* Image type tabs */}
      <div className="flex shrink-0 items-center gap-1">
        {availableTabs.map((tab) => (
          <button
            key={tab.key}
            type="button"
            onClick={() => {
              setActiveTab(tab.key);
              setSelectedImage(null);
              setTextlessOnly(false);
            }}
            className={cn(
              "rounded-md px-3 py-1.5 text-[12px] font-medium transition-colors",
              activeTab === tab.key
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:bg-muted hover:text-foreground",
            )}
          >
            {tab.label}
          </button>
        ))}

        {/* Textless filter — hidden for logos */}
        {activeTab !== "logo" && (
          <button
            type="button"
            onClick={() => setTextlessOnly(!textlessOnly)}
            className={cn(
              "ml-auto rounded-md px-2.5 py-1.5 text-[11px] font-medium transition-colors",
              textlessOnly
                ? "bg-primary/10 text-primary"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            Textless
          </button>
        )}
      </div>

      {/* Provider errors */}
      {providerErrors && Object.keys(providerErrors).length > 0 && (
        <div className="flex shrink-0 items-center gap-2 rounded-lg border border-amber-500/20 bg-amber-500/5 px-3 py-1.5 text-[11px] text-amber-600 dark:text-amber-400">
          <AlertCircle className="size-3.5 shrink-0" />
          <span>
            Could not load from{" "}
            {Object.keys(providerErrors)
              .map((k) => k.toUpperCase())
              .join(", ")}
          </span>
        </div>
      )}

      {/* Image grid */}
      {isLoading ? (
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="text-muted-foreground size-6 animate-spin" />
        </div>
      ) : isError ? (
        <div className="flex flex-1 items-center justify-center">
          <p className="text-muted-foreground text-sm">Failed to load images.</p>
        </div>
      ) : filteredImages.length === 0 ? (
        <div className="flex flex-1 items-center justify-center">
          <p className="text-muted-foreground text-sm">
            {textlessOnly ? "No textless images available." : "No images available."}
          </p>
        </div>
      ) : (
        <ScrollArea className="min-h-0 flex-1">
          <div
            className={cn(
              "grid gap-2 pr-3 pb-1",
              activeTab === "poster" ? "grid-cols-3 sm:grid-cols-4" : "grid-cols-2 sm:grid-cols-3",
            )}
          >
            {filteredImages.map((img, index) => {
              const isSelected = selectedImage === img;
              const isCurrent =
                img.original_url === currentOriginalUrl || img.original_url === currentStoredPath;

              return (
                <button
                  key={`${img.provider_id}-${img.original_url}-${index}`}
                  type="button"
                  onClick={() => setSelectedImage(isSelected ? null : img)}
                  className={cn(
                    "group relative overflow-hidden rounded-lg border transition-all",
                    isSelected
                      ? "border-primary ring-primary/30 ring-2"
                      : isCurrent
                        ? "border-primary/50"
                        : "border-border hover:border-foreground/20",
                  )}
                >
                  <div className={cn(tabConfig.aspect, "bg-muted")}>
                    <img
                      src={img.url}
                      alt=""
                      loading="lazy"
                      className="absolute inset-0 h-full w-full object-cover"
                      onError={(e) => {
                        e.currentTarget.style.display = "none";
                      }}
                    />
                  </div>

                  {/* Current indicator */}
                  {isCurrent && (
                    <div className="bg-primary/90 text-primary-foreground absolute top-1 left-1 flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[9px] font-semibold">
                      <Check className="size-2.5" />
                      Current
                    </div>
                  )}

                  {/* Selected indicator */}
                  {isSelected && !isCurrent && (
                    <div className="bg-primary text-primary-foreground absolute top-1 right-1 flex size-5 items-center justify-center rounded-full">
                      <Check className="size-3" />
                    </div>
                  )}

                  {/* Provider badge */}
                  <div className="absolute right-1 bottom-1">
                    <Badge
                      variant="secondary"
                      className="bg-black/60 px-1 py-0 text-[9px] text-white backdrop-blur-sm"
                    >
                      {img.provider_id.toUpperCase()}
                    </Badge>
                  </div>
                </button>
              );
            })}
          </div>
        </ScrollArea>
      )}

      {/* Apply button */}
      {selectedImage && (
        <div className="border-border/50 shrink-0 border-t pt-3">
          <Button
            onClick={handleApply}
            disabled={applyMutation.isPending}
            className="w-full"
            size="sm"
          >
            {applyMutation.isPending ? (
              <>
                <Loader2 className="size-4 animate-spin" />
                Applying...
              </>
            ) : (
              `Apply ${activeTab.charAt(0).toUpperCase() + activeTab.slice(1)}`
            )}
          </Button>
        </div>
      )}
    </div>
  );
}
