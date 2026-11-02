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

      setError("Incorrect PIN");
      setPin("");
    } catch (err) {
      if (attemptRef.current !== attempt) {
        return;
      }
      const lockout = pinLockoutMessage(err);
      if (lockout) {
        setPin("");
      }
      setError(lockout ?? "Verification failed");
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
          <DialogTitle>Enter PIN for {profile?.name}</DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor={pinInputId}>PIN</Label>
            <Input
              id={pinInputId}
              type="password"
              inputMode="numeric"
              maxLength={4}
              placeholder="Enter 4-digit PIN"
              value={pin}
              onChange={(event) => setPin(event.target.value)}
              autoFocus
            />
          </div>

          {error ? <p className="text-destructive text-sm">{error}</p> : null}

          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={handleClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={verifying || pin.length === 0}>
              {verifying ? "Verifying..." : "Confirm"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
