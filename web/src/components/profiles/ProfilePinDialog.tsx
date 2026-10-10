import { useId, useState } from "react";
import type { FormEvent } from "react";

import type { Profile } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

import { pinLockoutMessage } from "./pinLockout";

interface ProfilePinDialogProps {
  profile: Profile | null;
  onClose: () => void;
  onVerified: (profile: Profile, token: string) => void;
  verifyPin: (
    profileId: string,
    pin: string,
  ) => Promise<{ valid: boolean; profile_token?: string }>;
}

export function ProfilePinDialog({
  profile,
  onClose,
  onVerified,
  verifyPin,
}: ProfilePinDialogProps) {
  const [pin, setPin] = useState("");
  const [error, setError] = useState("");
  const [verifying, setVerifying] = useState(false);
  const pinInputId = useId();

  function handleClose() {
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
    try {
      const response = await verifyPin(profile.id, pin);
      if (response.valid && response.profile_token) {
        setPin("");
        onVerified(profile, response.profile_token);
        return;
      }

      setError("Incorrect PIN");
      setPin("");
    } catch (err) {
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
