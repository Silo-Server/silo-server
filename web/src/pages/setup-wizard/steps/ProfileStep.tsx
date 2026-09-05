import { useState } from "react";
import type { FormEvent } from "react";
import { createProfile } from "@/hooks/queries/profiles";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";
import { useWizardContext } from "../WizardContext";

export function ProfileStep() {
  const { selectProfile, refetchProfiles } = useWizardContext();
  const [profileName, setProfileName] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      const created = await createProfile({ name: profileName });
      selectProfile(created);
      refetchProfiles();
      toast.success("Profile created");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to create profile");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="setup-profile-name" className="text-xs">
          Name
        </Label>
        <Input
          id="setup-profile-name"
          value={profileName}
          onChange={(e) => setProfileName(e.target.value)}
          placeholder="Alex"
          required
        />
      </div>
      <div className="pt-3">
        <Button type="submit" disabled={submitting}>
          {submitting ? "Creating..." : "Create profile"}
        </Button>
      </div>
    </form>
  );
}
