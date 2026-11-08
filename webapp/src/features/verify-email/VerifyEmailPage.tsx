import { useAsyncResource } from "../../app/AsyncResource";
import { EmailVerificationContent } from "../../components/EmailVerification/EmailVerificationContent";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { message } from "../../i18n/messages";
import {
  completeEmailVerification,
  inspectEmailVerification,
  type EmailVerificationInspection,
} from "./VerifyEmailApi";

export interface VerifyEmailPageProps {
  token?: string;
}

const unknownAddress: EmailVerificationInspection = { kind: "unknown" };

export function VerifyEmailPage({ token }: VerifyEmailPageProps) {
  // The read-only lookup only names the address; the explicit action never
  // waits for it.
  const inspection = useAsyncResource(
    () => (token === undefined ? Promise.resolve(unknownAddress) : inspectEmailVerification(token)),
    unknownAddress,
    token !== undefined,
  );
  return (
    <AccessPageShell skipLabel={message("webapp.verify_email.skip_to_main")}>
      <EmailVerificationContent
        address={inspection.value.kind === "known" ? inspection.value.email : undefined}
        token={token}
        verifyEmail={completeEmailVerification}
      />
    </AccessPageShell>
  );
}
