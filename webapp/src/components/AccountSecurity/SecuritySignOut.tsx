import { useState } from "react";

import type { SecurityMutationResult } from "../../features/account-security/AccountSecurityApi";
import { message } from "../../i18n/messages";
import { Button } from "../Button/Button";
import styles from "./AccountSecurity.module.css";

/**
 * Sign-out for the current Session. The page places it quietly in the shell
 * footer, or in the task column when the footer has no confirmed name (a
 * recovery-restricted Session). A failure stays beside the control.
 */
export function SecuritySignOut({ signOut, variant = "text" }: {
  signOut(): Promise<SecurityMutationResult>;
  variant?: "text" | "secondary";
}) {
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);

  async function run() {
    if (pending) return;
    setPending(true); setFailed(false);
    const result = await signOut();
    if (result.kind === "success" || result.kind === "no_session") {
      window.location.replace("/login");
      return;
    }
    setFailed(true);
    setPending(false);
  }

  return <>
    <Button className={variant === "text" ? styles.signOut : undefined} variant={variant}
      isLoading={pending} loadingLabel={message("webapp.security.working")} onClick={run}>
      {message("webapp.security.sign_out")}
    </Button>
    <span className={styles.signOutFailure} role="status" aria-live="polite">
      {failed ? message("webapp.security.sign_out_failed") : null}
    </span>
  </>;
}
