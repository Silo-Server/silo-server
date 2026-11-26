import { Link, type To } from "react-router";
import { useAppKeyCapability } from "@/hooks/queries/appKeySession";
import { useT } from "@/i18n";

/**
 * The sentence every description of signing out, or of changing or
 * resetting a password, carries: those end sessions but not a device's
 * Silo app key, so the device signs in again by itself. The link goes to
 * the devices list where one is removed. `onNavigate` closes the dialog the
 * sentence sits in. It is a line of its own inside the text it follows.
 * Shown only while the server signs devices in with their keys: with app key
 * sign-in off the devices don't come back.
 */
export function SignsBackInNote({
  to,
  onNavigate,
  className,
}: {
  to: To;
  onNavigate?: () => void;
  className?: string;
}) {
  const t = useT();
  const capability = useAppKeyCapability();
  if (capability.data?.state !== "available") return null;
  return (
    <span className={className ?? "mt-1 block"}>
      {t("web.settings.appKeys.signsBackIn", {
        link: (chunks) => (
          <Link to={to} onClick={onNavigate} className="underline underline-offset-4">
            {chunks}
          </Link>
        ),
      })}
    </span>
  );
}
