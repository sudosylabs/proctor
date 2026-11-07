import type { ReactNode } from "react";

import proctorLockupDark from "../../assets/brand/proctor-lockup-purple-white.svg";
import proctorLockupLight from "../../assets/brand/proctor-lockup.svg";
import { useHostedInstitution } from "../../auth/HostedInstitution";
import { message } from "../../i18n/messages";
import { useProductTheme } from "../../theme/ProductTheme";
import styles from "./AccessPageShell.module.css";

export interface AccessPageSignedIn {
  /** Safe display name of the signed-in User, rendered as escaped text. */
  name: string;
  /** Page-owned sign-out control (a button or link) placed after the name. */
  signOut?: ReactNode;
}

export interface AccessPageShellProps {
  children: ReactNode;
  /** Present only when the page has confirmed a signed-in identity. */
  signedIn?: AccessPageSignedIn;
  skipLabel: string;
}

export function AccessPageShell({
  children,
  signedIn,
  skipLabel,
}: AccessPageShellProps) {
  const { effectiveTheme } = useProductTheme();
  const institution = useHostedInstitution();
  const lockup =
    effectiveTheme.colorScheme === "dark"
      ? proctorLockupDark
      : proctorLockupLight;

  return (
    <>
      <a className="proctor-skip-link" href="#main-content">
        {skipLabel}
      </a>
      <div className={styles.shell}>
        <header className={styles.masthead}>
          <img
            className={styles.brand}
            src={lockup}
            alt="Proctor"
            data-proctor-lockup-color-scheme={effectiveTheme.colorScheme}
            translate="no"
            width="163"
            height="32"
          />
          {institution.displayName === undefined ? null : (
            <p className={styles.institution} data-proctor-shell-institution="">
              {institution.displayName}
            </p>
          )}
        </header>

        <main id="main-content" className={styles.main} tabIndex={-1}>
          {children}
        </main>

        <footer className={styles.footer}>
          <div className={styles.footerRule}>
            {signedIn === undefined ? null : (
              <p className={styles.signedIn}>
                <span>
                  {message("webapp.shell.signed_in", { Name: signedIn.name })}
                </span>
                {signedIn.signOut === undefined ? null : (
                  <>
                    <span aria-hidden="true">·</span>
                    {signedIn.signOut}
                  </>
                )}
              </p>
            )}
            <p className={styles.help}>
              <HelpLine
                displayName={institution.displayName}
                supportAddress={institution.supportAddress}
              />
            </p>
          </div>
        </footer>
      </div>
    </>
  );
}

function HelpLine({
  displayName,
  supportAddress,
}: {
  displayName?: string;
  supportAddress?: string;
}) {
  if (displayName === undefined) {
    return message("webapp.shell.help.unnamed");
  }
  const placeholder = "{{.Institution}}";
  const template = message("webapp.shell.help.institution");
  const index = template.indexOf(placeholder);
  if (supportAddress === undefined || index < 0) {
    return message("webapp.shell.help.institution", {
      Institution: displayName,
    });
  }
  return (
    <>
      {template.slice(0, index)}
      <a href={`mailto:${supportAddress}`}>{displayName}</a>
      {template.slice(index + placeholder.length)}
    </>
  );
}
