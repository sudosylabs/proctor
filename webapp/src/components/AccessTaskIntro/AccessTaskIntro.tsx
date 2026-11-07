import type { ReactNode } from "react";

import { TaskHeading } from "../TaskState/TaskState";
import styles from "./AccessTaskIntro.module.css";

/** State tones for a kind line that names a real terminal outcome. */
export type AccessTaskKindTone = "success" | "warning" | "danger";

export interface AccessTaskIntroProps {
  body: ReactNode;
  focusHeading?: boolean;
  heading: ReactNode;
  headingID: string;
  /** Quiet sentence-case line naming the kind of task, above the heading. */
  kind: ReactNode;
  /** Colours the kind line only when it reports a true state outcome. */
  kindTone?: AccessTaskKindTone;
}

export function AccessTaskIntro({
  body,
  focusHeading = false,
  heading,
  headingID,
  kind,
  kindTone,
}: AccessTaskIntroProps) {
  return (
    <header className={styles.intro}>
      <p
        className={kindTone === undefined ? styles.kind : `${styles.kind} ${styles[kindTone]}`}
        data-proctor-kind-tone={kindTone}
      >
        {kind}
      </p>
      <TaskHeading focus={focusHeading} id={headingID}>
        {heading}
      </TaskHeading>
      <p className={styles.body}>{body}</p>
    </header>
  );
}
