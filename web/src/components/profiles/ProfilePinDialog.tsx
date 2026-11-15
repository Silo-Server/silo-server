import { useId, useRef, useState } from "react";
import type { FormEvent } from "react";

import type { Profile } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

import { useT } from "@/i18n";

import { pinLockoutMessage } from "./pinLockout";

interface ProfilePinDialogProps {
  profile: Profile | null;
  /** Why the PIN is asked for, when it isn't to switch to the profile. */
  description?: string;
  onClose: () => void;
  onVerified: (profile: Profile, token: string) => void;
  verifyPin: (
    profileId: string,
    pin: string,
  ) => Promise<{ valid: boolean; profile_token?: string }>;
}

export function ProfilePinDialog({
  profile,
  description,
  onClose,
  onVerified,
  verifyPin,
}: ProfilePinDialogProps) {
  const t = useT();
  const [pin, setPin] = useState("");
  const [error, setError] = useState("");
  const [verifying, setVerifying] = useState(false);
  const pinInputId = useId();
  // Advances when the dialog closes, so a check still in flight then is
  // ignored: cancelling must not switch profiles or open what the PIN unlocks.
  const attemptRef = useRef(0);

  function handleClose() {
    attemptRef.current += 1;
    setPin("");
    setError("");
    onClose();
  }

  async function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (!profile || pin.length === 0) {
      return;
    }

    setError("");
    setVerifying(true);
    const attempt = attemptRef.current;
    try {
      const response = await verifyPin(profile.id, pin);
      if (attemptRef.current !== attempt) {
        return;
      }
      if (response.valid && response.profile_token) {
        setPin("");
        onVerified(profile, response.profile_token);
        return;
      }

      setError(t("web.profiles.pin.incorrect"));
      setPin("");
    } catch (err) {
      if (attemptRef.current !== attempt) {
        return;
      }
      const lockout = pinLockoutMessage(err);
      if (lockout) {
        setPin("");
      }
      setError(lockout ?? t("web.profiles.pin.verifyFailed"));
    } finally {
      setVerifying(false);
    }
  }

  return (
    <Dialog
      open={profile !== null}
      onOpenChange={(open) => {
        if (!open) {
          handleClose();
        }
      }}
    >
      <DialogContent className="sm:max-w-xs">
        <DialogHeader>
          <DialogTitle>{t("web.profiles.pin.title", { name: profile?.name ?? "" })}</DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor={pinInputId}>{t("web.profiles.pin.label")}</Label>
            <Input
              id={pinInputId}
              type="password"
              inputMode="numeric"
              maxLength={4}
              placeholder={t("web.profiles.pin.placeholder")}
              value={pin}
              onChange={(event) => setPin(event.target.value)}
              autoFocus
            />
          </div>

          {error ? <p className="text-destructive text-sm">{error}</p> : null}

          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={handleClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={verifying || pin.length === 0}>
              {verifying ? t("web.profiles.pin.verifying") : t("web.profiles.pin.confirm")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
