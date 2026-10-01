import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { cn } from "@/lib/utils";
import { MEMBER_TINTS, memberInitials, type MemberTint } from "./members";

/** A member's initials in their room tint. */
export function MemberAvatar({
  name,
  tint = MEMBER_TINTS[0],
  size = "default",
  className,
  title,
  solid = false,
}: {
  name: string;
  tint?: MemberTint;
  size?: "sm" | "default" | "lg";
  className?: string;
  title?: string;
  solid?: boolean;
}) {
  return (
    <Avatar size={size} className={className} title={title ?? name}>
      <AvatarFallback
        className={cn("font-semibold", solid ? [tint.solidBg, "text-white"] : [tint.bg, tint.text])}
      >
        {memberInitials(name)}
      </AvatarFallback>
    </Avatar>
  );
}
