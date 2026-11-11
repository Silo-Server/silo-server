import { useEffect, useRef, useState } from "react";
import { useBlocker } from "react-router";

import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

import { ACCESS_CARD_TITLE_KEYS, useCardEditing } from "./cardEditing";
import { parseUserDetailTab } from "./userDetailTabs";

function tabOf(search: string) {
  return parseUserDetailTab(new URLSearchParams(search).get("tab"));
}

/**
 * Asks before leaving a card with unsaved changes: another page, or another
 * tab of this one (only the open tab is mounted, so a tab switch would drop
 * the draft). A filter or sub-view change within the tab is not blocked.
 */
export function UnsavedCardGuard() {
  const t = useT();
  const { active } = useCardEditing();
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      active !== null &&
      active.changeCount > 0 &&
      (currentLocation.pathname !== nextLocation.pathname ||
        tabOf(currentLocation.search) !== tabOf(nextLocation.search)),
  );
  const [saving, setSaving] = useState(false);
  // Set once the navigation is on its way, so closing the dialog does not
  // reset the blocker and keep the admin here.
  const proceeding = useRef(false);

  useEffect(() => {
    if (blocker.state !== "blocked") proceeding.current = false;
  }, [blocker.state]);

  const open = blocker.state === "blocked" && active !== null;
  const count = active?.changeCount ?? 0;

  function stay() {
    if (blocker.state === "blocked") blocker.reset();
  }
  function leave() {
    if (blocker.state !== "blocked") return;
    proceeding.current = true;
    blocker.proceed();
  }

  async function saveAndContinue() {
    if (!active || saving) return;
    setSaving(true);
    try {
      // A refused save leaves its error or conflict in the card.
      if (await active.save()) leave();
      else stay();
    } finally {
      setSaving(false);
    }
  }

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (next || proceeding.current || saving) return;
        stay();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("web.admin.users.detail.common.unsaved.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.admin.users.detail.common.unsaved.description", {
              count,
              card: active ? t(ACCESS_CARD_TITLE_KEYS[active.id]) : "",
              b: (chunks) => <strong className="text-foreground font-semibold">{chunks}</strong>,
            })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <Button variant="ghost" disabled={saving} onClick={stay}>
            {t("web.admin.users.detail.common.unsaved.keepEditing")}
          </Button>
          <Button
            variant="outline"
            disabled={saving}
            onClick={() => {
              active?.discard();
              leave();
            }}
          >
            {t("web.admin.users.detail.common.unsaved.discard")}
          </Button>
          <Button disabled={saving} onClick={() => void saveAndContinue()}>
            {saving
              ? t("web.admin.users.detail.common.saving")
              : t("web.admin.users.detail.common.unsaved.saveAndContinue")}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
