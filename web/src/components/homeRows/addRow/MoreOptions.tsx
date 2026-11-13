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
import { useT } from "@/i18n";
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
  webOnly = false,
  summary,
  onItemLimitChange,
  onHeroChange,
  onWebOnlyChange,
  children,
}: {
  itemLimit: number;
  /** Absent where rows have no hero banner (Requests > Discover Page). */
  hero?: boolean;
  /** The hero banner is kept off the apps. */
  webOnly?: boolean;
  /** What the kind's own options hold, first on the summary line ("Highest rated first"). */
  summary?: string;
  onItemLimitChange: (limit: number) => void;
  onHeroChange?: (hero: boolean) => void;
  /** With it, a hero row offers "Show in the apps" under Hero banner. */
  onWebOnlyChange?: (webOnly: boolean) => void;
  /** Kind-specific fields, shown above the common ones. */
  children?: ReactNode;
}) {
  const t = useT();
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
              {t("web.home.addRow.more.title")}
            </span>
            <span className="text-muted-foreground text-[13px] font-normal">
              {t("web.home.addRow.more.summary", {
                has_summary: summary ? "yes" : "no",
                summary: summary ?? "",
                titles: titleCount(itemLimit),
                has_hero: onHeroChange ? "yes" : "no",
                hero: hero
                  ? webOnly
                    ? t("web.home.addRow.more.heroWebOnly")
                    : t("web.home.addRow.more.hero")
                  : t("web.home.addRow.more.notHero"),
                dot: (chunks) => (
                  <span aria-hidden className="mx-1.5">
                    {chunks}
                  </span>
                ),
              })}
            </span>
          </span>
        </AccordionTrigger>
        <AccordionContent className="border-border bg-surface/50 grid gap-4 border-t px-4 pt-4 pb-4">
          {children}
          <div className="flex items-center justify-between gap-4">
            <div>
              <Label htmlFor={`${id}-limit`}>{t("web.home.addRow.more.itemLimit")}</Label>
              <p className="text-muted-foreground mt-1 text-[13px]">
                {t("web.home.addRow.more.itemLimitHint")}
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
          {onHeroChange ? (
            <div className="border-border/70 flex items-center justify-between gap-4 border-t pt-4">
              <div>
                <Label htmlFor={`${id}-hero`}>{t("web.home.addRow.more.heroBanner")}</Label>
                <p className="text-muted-foreground mt-1 text-[13px]">
                  {onWebOnlyChange
                    ? t("web.home.addRow.more.heroHintWeb")
                    : t("web.home.addRow.more.heroHint")}
                </p>
              </div>
              <Switch
                id={`${id}-hero`}
                checked={hero ?? false}
                onCheckedChange={(checked) => onHeroChange(checked === true)}
              />
            </div>
          ) : null}
          {hero && onHeroChange && onWebOnlyChange ? (
            <div className="border-border/70 ms-4 flex items-center justify-between gap-4 border-s-2 ps-4">
              <div>
                <Label htmlFor={`${id}-apps`}>{t("web.home.addRow.more.showInApps")}</Label>
                <p className="text-muted-foreground mt-1 text-[13px]">
                  {webOnly
                    ? t("web.home.addRow.more.webOnlyHint")
                    : t("web.home.addRow.more.appsHint")}
                </p>
              </div>
              <Switch
                id={`${id}-apps`}
                checked={!webOnly}
                onCheckedChange={(checked) => onWebOnlyChange(checked !== true)}
              />
            </div>
          ) : null}
        </AccordionContent>
      </AccordionItem>
    </Accordion>
  );
}
