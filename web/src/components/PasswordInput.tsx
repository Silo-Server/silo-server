import { useState, forwardRef } from "react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Eye, EyeOff } from "lucide-react";
import { useT } from "@/i18n";

export const PasswordInput = forwardRef<HTMLInputElement, React.ComponentProps<typeof Input>>(
  function PasswordInput(props, ref) {
    const [visible, setVisible] = useState(false);
    const t = useT();
    return (
      <div className="relative">
        <Input ref={ref} {...props} type={visible ? "text" : "password"} className="pr-10" />
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="text-muted-foreground hover:text-foreground absolute top-0 right-0 h-full w-10"
          onClick={() => setVisible((v) => !v)}
          aria-label={t(visible ? "web.auth.passwordInput.hide" : "web.auth.passwordInput.show")}
          aria-pressed={visible}
        >
          {visible ? (
            <EyeOff className="h-4 w-4" aria-hidden="true" />
          ) : (
            <Eye className="h-4 w-4" aria-hidden="true" />
          )}
        </Button>
      </div>
    );
  },
);
