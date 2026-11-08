import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { ForgotPasswordContent } from "../../components/PasswordRecovery/ForgotPasswordContent";
import { message } from "../../i18n/messages";
import { requestPasswordReset } from "./ForgotPasswordApi";

export function ForgotPasswordPage() {
  return (
    <AccessPageShell skipLabel={message("webapp.forgot_password.skip_to_main")}>
      <ForgotPasswordContent requestReset={requestPasswordReset} />
    </AccessPageShell>
  );
}
