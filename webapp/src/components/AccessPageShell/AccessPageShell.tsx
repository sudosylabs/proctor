import {
  createContext,
  type ReactNode,
  useContext,
  useState,
} from "react";
import { createPortal } from "react-dom";

import proctorLockupDark from "../../assets/brand/proctor-lockup-dark.svg";
import proctorLockupLight from "../../assets/brand/proctor-lockup-light.svg";
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
  /** Page-owned verification rail items, rendered before the shell's help item. */
  rail?: ReactNode;
  /** Present only when the page has confirmed a signed-in identity. */
  signedIn?: AccessPageSignedIn;
  skipLabel: string;
}

export function AccessPageShell({
  children,
  rail,
  signedIn,
  skipLabel,
}: AccessPageShellProps) {
  const { effectiveTheme } = useProductTheme();
  const institution = useHostedInstitution();
  const [railSlot, setRailSlot] = useState<HTMLElement | null>(null);
  const lockup =
    effectiveTheme.colorScheme === "dark"
      ? proctorLockupDark
      : proctorLockupLight;

  return (
    <>
      <a className="proctor-skip-link" href="#main-content">
        {skipLabel}
      </a>
      <div className={styles.frame}>
        <header
          className={styles.masthead}
          data-proctor-shell-signed-in={signedIn === undefined ? undefined : ""}
        >
          <div className={styles.identity}>
            <img
              className={styles.brand}
              src={lockup}
              alt="Proctor"
              data-proctor-lockup-color-scheme={effectiveTheme.colorScheme}
              translate="no"
              width="143"
              height="28"
            />
            {institution.displayName === undefined ? null : (
              <p className={styles.institution} data-proctor-shell-institution="">
                {institution.displayName}
              </p>
            )}
          </div>
          {signedIn === undefined ? null : (
            <div className={styles.signedIn}>
              <p className={styles.signedInText}>
                <span className={styles.signedInFull}>
                  {message("webapp.shell.signed_in", { Name: signedIn.name })}
                </span>
                <span className={styles.signedInShort} aria-hidden="true">
                  {signedIn.name}
                </span>
              </p>
              {signedIn.signOut}
            </div>
          )}
        </header>

        <main id="main-content" className={styles.task} tabIndex={-1}>
          <RailSlotContext.Provider value={railSlot}>{children}</RailSlotContext.Provider>
        </main>

        <aside className={styles.rail} aria-label={message("webapp.shell.rail.label")}>
          {rail}
          <div className={styles.railSlot} ref={setRailSlot} />
          <RailItem>
            <p>
              <HelpLine
                displayName={institution.displayName}
                supportAddress={institution.supportAddress}
              />
            </p>
          </RailItem>
        </aside>
      </div>
    </>
  );
}

const RailSlotContext = createContext<HTMLElement | null>(null);

/**
 * Places rail items owned by nested task content into the verification rail,
 * after the page's own `rail` items and before the shell's help item. A page
 * that already knows its items passes them through the shell's `rail` prop.
 */
export function TaskRail({ children }: { children: ReactNode }) {
  const slot = useContext(RailSlotContext);
  return slot === null ? null : createPortal(children, slot);
}

export interface RailItemProps {
  children: ReactNode;
  heading?: ReactNode;
  /**
   * Marks information a person needs before acting. Below `48rem` the rail
   * follows the task, so the page repeats this as a note in its intro and the
   * rail item is hidden there.
   */
  beforeTask?: boolean;
}

/** One verification rail entry: an optional small heading and its content. */
export function RailItem({ beforeTask = false, children, heading }: RailItemProps) {
  return (
    <section
      className={beforeTask ? `${styles.railItem} ${styles.beforeTask}` : styles.railItem}
      data-proctor-rail-item=""
      data-proctor-rail-before-task={beforeTask ? "" : undefined}
    >
      {heading === undefined ? null : <h2>{heading}</h2>}
      {children}
    </section>
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
