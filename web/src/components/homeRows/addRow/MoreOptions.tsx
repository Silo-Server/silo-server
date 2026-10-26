import { useId, useState, type ReactNode } from "react";
import { SlidersHorizontal } from "lucide-react";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { titleCount } from "@/lib/homeRows/describe";

/** Most Home rows hold up to 50 titles; the editor has always allowed up to 100. */
const MAX_ITEM_LIMIT = 100;

/**
 * The collapsed "More options" box: the row's settings that rarely change,
 * with a one-line summary so a closed box still says what it holds.
 */
export function MoreOptions({
  itemLimit,
  hero,
  summary,
  onItemLimitChange,
  onHeroChange,
  children,
}: {
  itemLimit: number;
  hero: boolean;
  /** What the kind's own options hold, first on the summary line ("Highest rated first"). */
  summary?: string;
  onItemLimitChange: (limit: number) => void;
  onHeroChange: (hero: boolean) => void;
  /** Kind-specific fields, shown above the common ones. */
  children?: ReactNode;
}) {
  const id = useId();
  // What the box holds while it is being typed in; it may be blank or out of
  // range for a moment. Only a whole number from 1 to 100 reaches the draft.
  const [typedLimit, setTypedLimit] = useState<string | null>(null);
  return (
    <Accordion type="single" collapsible className="border-border rounded-[14px] border">
      <AccordionItem value="more" className="border-b-0">
        <AccordionTrigger className="items-center px-4 py-3 hover:no-underline">
          <span className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2.5">
            <span className="inline-flex items-center gap-2.5 font-semibold">
              <SlidersHorizontal aria-hidden className="text-muted-foreground size-4 self-center" />
              More options
            </span>
            <span className="text-muted-foreground text-[13px] font-normal">
              {summary ? (
                <>
                  {summary}
                  <span aria-hidden className="mx-1.5">
                    ·
                  </span>
                </>
              ) : null}
              {titleCount(itemLimit)}
              <span aria-hidden className="mx-1.5">
                ·
              </span>
              {hero ? "the hero banner" : "not the hero banner"}
            </span>
          </span>
        </AccordionTrigger>
        <AccordionContent className="border-border bg-surface/50 grid gap-4 border-t px-4 pt-4 pb-4">
          {children}
          <div className="flex items-center justify-between gap-4">
            <div>
              <Label htmlFor={`${id}-limit`}>Number of titles</Label>
              <p className="text-muted-foreground mt-1 text-[13px]">
                The most titles the row shows.
              </p>
            </div>
            <Input
              id={`${id}-limit`}
              type="number"
              inputMode="numeric"
              min={1}
              max={MAX_ITEM_LIMIT}
              className="w-24"
              value={typedLimit ?? itemLimit}
              onChange={(event) => {
                setTypedLimit(event.target.value);
                const value = Number(event.target.value);
                if (Number.isInteger(value) && value >= 1 && value <= MAX_ITEM_LIMIT)
                  onItemLimitChange(value);
              }}
              onBlur={() => setTypedLimit(null)}
            />
          </div>
          <div className="border-border/70 flex items-center justify-between gap-4 border-t pt-4">
            <div>
              <Label htmlFor={`${id}-hero`}>Hero banner</Label>
              <p className="text-muted-foreground mt-1 text-[13px]">
                The web shows this row as a banner. The apps show it as a regular row.
              </p>
            </div>
            <Switch
              id={`${id}-hero`}
              checked={hero}
              onCheckedChange={(checked) => onHeroChange(checked === true)}
            />
          </div>
        </AccordionContent>
      </AccordionItem>
    </Accordion>
  );
}
