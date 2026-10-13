import { ChevronRight } from "lucide-react";
import { Link } from "react-router";

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { cn } from "@/lib/utils";

/**
 * Whose settings these are. Almost every personal setting is stored per
 * profile, so the settings area names the profile and links to the picker.
 */
export function CurrentProfileLink({ className }: { className?: string }) {
  const { profile } = useCurrentProfile();
  const profileName = profile?.name ?? "Your profile";

  return (
    <Link
      to="/profiles"
      aria-label={`Current profile: ${profileName}`}
      className={cn(
        "hover:bg-accent/70 focus-visible:ring-ring/60 flex items-center gap-2.5 rounded-xl px-2 py-1.5 transition-colors focus-visible:ring-[3px] focus-visible:outline-none",
        className,
      )}
    >
      <Avatar className="border-border h-8 w-8 border">
        {profile?.avatar_url ? <AvatarImage src={profile.avatar_url} alt="" /> : null}
        <AvatarFallback className="bg-accent text-foreground text-xs font-semibold">
          {profileName.charAt(0).toUpperCase()}
        </AvatarFallback>
      </Avatar>
      <span className="min-w-0 flex-1">
        <span className="text-muted-foreground block text-xs leading-tight">Current profile</span>
        <span className="block truncate text-sm font-semibold">{profileName}</span>
      </span>
      <ChevronRight className="text-muted-foreground h-4 w-4 shrink-0" aria-hidden="true" />
    </Link>
  );
}
