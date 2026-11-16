import type { ReactNode } from "react";

import styles from "./FormGroup.module.css";

export interface FormGroupProps {
  children: ReactNode;
  heading: ReactNode;
  /** Stable id for the group heading. */
  headingID: string;
  /** One-based position, shown as a muted two-digit numeral. */
  number: number;
  /**
   * `fieldset` when the group's controls answer one question together (form
   * groups); `section` when it is a step that holds its own content.
   */
  as?: "fieldset" | "section";
}

/** A numbered group in a long task, separated from its neighbours by a hairline. */
export function FormGroup({ as = "fieldset", children, heading, headingID, number }: FormGroupProps) {
  const title = (
    <>
      <span className={styles.number} aria-hidden="true">
        {String(number).padStart(2, "0")}
      </span>
      <span>{heading}</span>
    </>
  );
  if (as === "section") {
    return (
      <section className={styles.group} aria-labelledby={headingID} data-proctor-form-group="">
        <h2 className={styles.heading} id={headingID}>{title}</h2>
        <div className={styles.body}>{children}</div>
      </section>
    );
  }
  return (
    <fieldset className={styles.group} aria-labelledby={headingID} data-proctor-form-group="">
      <legend className={styles.legend}>
        <span className={styles.heading} id={headingID}>{title}</span>
      </legend>
      <div className={styles.body}>{children}</div>
    </fieldset>
  );
}
