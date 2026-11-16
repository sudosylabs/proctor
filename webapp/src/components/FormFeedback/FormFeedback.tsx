import type { ReactNode } from "react";

import { Icon } from "../Icon/Icon";
import styles from "./FormFeedback.module.css";

export interface FormFeedbackProps {
  className?: string;
  id?: string;
  message?: ReactNode;
}

/**
 * The live region stays mounted while empty so a new message is announced
 * without moving focus. Features must not repeat the message in another live
 * region or focus this one to make it speak. Failures tied to one field belong
 * on that field, not here.
 */
export function FormFeedback({ className, id, message }: FormFeedbackProps) {
  return (
    <div
      aria-atomic="true"
      aria-live="polite"
      className={classes(styles.feedback, className)}
      data-proctor-form-feedback=""
      id={id}
      role="status"
    >
      {message === undefined ? null : (
        <p className={styles.message}>
          <Icon className={styles.icon} name="error" />
          <span>{message}</span>
        </p>
      )}
    </div>
  );
}

function classes(...values: Array<string | undefined>) {
  return values.filter((value): value is string => value !== undefined).join(" ");
}
