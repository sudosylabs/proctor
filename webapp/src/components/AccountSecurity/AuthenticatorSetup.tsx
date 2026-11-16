// Copyright (c) 2026 Sudosy Labs. SPDX-License-Identifier: AGPL-3.0-only
import { QRCodeSVG } from "qrcode.react";
import { useState } from "react";

import type { AuthenticatorSetup as Setup } from "../../features/account-security/AccountSecurityApi";
import { groupSetupKey } from "../../features/account-security/presentation";
import { message } from "../../i18n/messages";
import { Button } from "../Button/Button";
import styles from "./AuthenticatorSetup.module.css";

export function AuthenticatorSetup({ setup }: { setup: Setup }) {
  const [copyStatus, setCopyStatus] = useState<"copied" | "failed">();

  async function copyKey() {
    try {
      // The ungrouped key, so it pastes straight into an authenticator app.
      await navigator.clipboard.writeText(setup.secret);
      setCopyStatus("copied");
    } catch {
      setCopyStatus("failed");
    }
  }

  return <div className={styles.setup}>
    <QRCodeSVG className={styles.qr} value={setup.provisioningURI}
      size={240} level="M" marginSize={4}
      bgColor="var(--proctor-color-machine-readable-background)"
      fgColor="var(--proctor-color-machine-readable-foreground)"
      role="img" aria-label={message("webapp.security.setup.qr_label")} />
    <div className={styles.manual}>
      <p className={styles.manualHelp} id="authenticator-secret-label">{message("webapp.security.setup.manual")}</p>
      <p className={styles.key}>
        <span className={styles.visuallyHidden}>{message("webapp.security.setup.key")}</span>
        <code id="authenticator-secret" translate="no">{groupSetupKey(setup.secret)}</code>
      </p>
      <div className={styles.copy}>
        <Button variant="secondary" className={styles.copyButton} onClick={copyKey}
          aria-describedby="authenticator-secret">
          {message("webapp.security.setup.copy_key")}
        </Button>
        <p className={styles.copyStatus} role="status" aria-live="polite">
          {copyStatus === undefined ? null : message(copyStatus === "copied"
            ? "webapp.security.setup.key_copied" : "webapp.security.setup.copy_failed")}
        </p>
      </div>
    </div>
  </div>;
}
