import { useId, useState } from "react";
import { Plus, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { OverlayDef, OverlayImage, OverlayItemConfig } from "@/lib/overlays";

const NONE = "__none__";

function ImageSelect({
  value,
  images,
  label,
  noneLabel,
  onChange,
}: {
  value: string | undefined;
  images: readonly OverlayImage[];
  label: string;
  noneLabel: string;
  onChange: (next: string | undefined) => void;
}) {
  return (
    <Select
      value={value ?? NONE}
      onValueChange={(next) => onChange(next === NONE ? undefined : next)}
    >
      <SelectTrigger className="w-full" aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={NONE}>{noneLabel}</SelectItem>
        {images.map((image) => (
          <SelectItem key={image.ref} value={image.ref}>
            <span className="flex items-center gap-2">
              <img src={image.url} alt="" className="h-4 w-auto max-w-12 object-contain" />
              <span className="truncate">{image.name}</span>
            </span>
          </SelectItem>
        ))}
        {/* A ref the library no longer holds still shows, so it can be cleared. */}
        {value && !images.some((image) => image.ref === value) ? (
          <SelectItem value={value}>Removed image</SelectItem>
        ) : null}
      </SelectContent>
    </Select>
  );
}

/**
 * Uploaded images on one badge: one in the icon's place, and per-value images
 * that replace the whole badge while its value matches ("2160p" → a 4K logo).
 */
export function BadgeImagesControl({
  def,
  config,
  images,
  onChange,
}: {
  def: OverlayDef;
  config: OverlayItemConfig;
  images: readonly OverlayImage[];
  onChange: (next: Partial<OverlayItemConfig>) => void;
}) {
  const [newKey, setNewKey] = useState("");
  const [newImage, setNewImage] = useState<string | undefined>(undefined);
  const suggestionsId = useId();
  const valueImages = config.valueImages ?? {};
  const entries = Object.entries(valueImages);
  const setValueImages = (next: Record<string, string>) =>
    onChange({ valueImages: Object.keys(next).length > 0 ? next : undefined });
  const trimmedKey = newKey.trim();
  const canAdd = trimmedKey.length > 0 && trimmedKey.length <= 64 && newImage !== undefined;

  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <p className="text-sm font-medium">Icon image</p>
        <ImageSelect
          value={config.image}
          images={images}
          label={`${def.label} icon image`}
          noneLabel="Built-in icon"
          onChange={(image) => onChange({ image })}
        />
      </div>
      {def.getValueKey ? (
        <div className="space-y-1.5">
          <p className="text-sm font-medium">Images for values</p>
          <p className="text-muted-foreground text-[13px] leading-relaxed">
            Replaces the whole badge while it shows that value.
          </p>
          {entries.map(([key, ref]) => (
            <div key={key} className="flex items-center gap-2">
              <span className="w-20 shrink-0 truncate text-sm" title={key}>
                {key}
              </span>
              <ImageSelect
                value={ref}
                images={images}
                label={`Image for ${key}`}
                noneLabel="No image"
                onChange={(next) => {
                  const rest = { ...valueImages };
                  if (next) rest[key] = next;
                  else delete rest[key];
                  setValueImages(rest);
                }}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={`Remove the image for ${key}`}
                onClick={() => {
                  const rest = { ...valueImages };
                  delete rest[key];
                  setValueImages(rest);
                }}
              >
                <X className="h-4 w-4" aria-hidden="true" />
              </Button>
            </div>
          ))}
          {entries.length < 32 ? (
            <div className="flex items-center gap-2">
              <Input
                value={newKey}
                onChange={(event) => setNewKey(event.target.value)}
                placeholder="Value"
                aria-label="Value"
                list={def.valueKeySuggestions ? suggestionsId : undefined}
                className="w-24 shrink-0"
                maxLength={64}
              />
              {def.valueKeySuggestions ? (
                <datalist id={suggestionsId}>
                  {def.valueKeySuggestions
                    .filter((suggestion) => !(suggestion in valueImages))
                    .map((suggestion) => (
                      <option key={suggestion} value={suggestion} />
                    ))}
                </datalist>
              ) : null}
              <ImageSelect
                value={newImage}
                images={images}
                label="Image for the new value"
                noneLabel="Pick an image"
                onChange={setNewImage}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label="Add the value image"
                disabled={!canAdd}
                onClick={() => {
                  if (!canAdd || newImage === undefined) return;
                  setValueImages({ ...valueImages, [trimmedKey]: newImage });
                  setNewKey("");
                  setNewImage(undefined);
                }}
              >
                <Plus className="h-4 w-4" aria-hidden="true" />
              </Button>
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
