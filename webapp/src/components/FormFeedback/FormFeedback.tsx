import type { ReactNode } from "react";

import { Icon } from "../Icon/Icon";
import styles from "./FormFeedback.module.css";

export interface FormFeedbackProps {
  className?: string;
  id?: string;
  message?: ReactNode;
}

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
