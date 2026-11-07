import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { ResetPasswordContent } from "../../components/PasswordRecovery/ResetPasswordContent";
import { message } from "../../i18n/messages";
import { completePasswordReset } from "./ResetPasswordApi";

export interface ResetPasswordPageProps {
  token?: string;
}

export function ResetPasswordPage({ token }: ResetPasswordPageProps) {
  return (
    <AccessPageShell skipLabel={message("webapp.reset_password.skip_to_main")}>
      <ResetPasswordContent
        token={token}
        completeReset={completePasswordReset}
      />
    </AccessPageShell>
  );
}
