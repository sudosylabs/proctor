import {
  type HTMLAttributes,
  type ReactNode,
  useEffect,
  useRef,
} from "react";

import { Icon, type IconName } from "../Icon/Icon";
import { Loading } from "../Loading/Loading";
import styles from "./TaskState.module.css";

export type TaskStatusTone = "success" | "warning" | "danger";

/** A real outcome shown above a task heading: colour, icon, and a word together. */
export interface TaskStatus {
  label: ReactNode;
  tone: TaskStatusTone;
}

const statusIcons: Record<TaskStatusTone, IconName> = {
  success: "stateSuccess",
  warning: "stateWarning",
  danger: "stateDanger",
};

export function TaskStatusLine({ status }: { status: TaskStatus }) {
  return (
    <p
      className={classes(styles.status, styles[status.tone])}
      data-proctor-status-tone={status.tone}
    >
      <Icon name={statusIcons[status.tone]} size="small" />
      <span>{status.label}</span>
    </p>
  );
}

export interface TaskStateProps {
  body: ReactNode;
  busy?: boolean;
  children?: ReactNode;
  className?: string;
  focusHeading?: boolean;
  heading: ReactNode;
  headingID: string;
  /** Present only when the state reports a real outcome. */
  status?: TaskStatus;
}

export function TaskState({
  body,
  busy = false,
  children,
  className,
  focusHeading = false,
  heading,
  headingID,
  status,
}: TaskStateProps) {
  return (
    <section
      aria-busy={busy || undefined}
      aria-labelledby={headingID}
      className={classes(styles.state, className)}
    >
      {status === undefined ? null : <TaskStatusLine status={status} />}
      <TaskHeading focus={focusHeading && !busy} id={headingID}>
        {heading}
      </TaskHeading>
      {busy ? (
        <div className={styles.loading}>
          <Loading label={body} showLabel size="large" announce={false} />
        </div>
      ) : <p className={styles.body}>{body}</p>}
      {children}
    </section>
  );
}

export interface TaskHeadingProps {
  children: ReactNode;
  className?: string;
  focus?: boolean;
  id: string;
}

/**
 * Focus only after a user-triggered replacement removed the control that
 * started it; initial loading and background updates must not move focus.
 */
export function TaskHeading({
  children,
  className,
  focus = false,
  id,
}: TaskHeadingProps) {
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    if (!focus) {
      return;
    }
    const frame = requestAnimationFrame(() => headingRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [focus]);

  return (
    <h1 className={className} ref={headingRef} id={id} tabIndex={focus ? -1 : undefined}>
      {children}
    </h1>
  );
}

export function TaskStateActions({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div {...props} className={classes(styles.actions, className)} />
  );
}

/**
 * Keep this mounted while the task changes so state changes are announced.
 * Announce only bounded localized copy, never credentials, identifiers or
 * server prose.
 */
export function TaskStateAnnouncement({ message }: { message: string }) {
  return (
    <div
      className={styles.visuallyHidden}
      role="status"
      aria-atomic="true"
      aria-live="polite"
    >
      {message}
    </div>
  );
}

function classes(...values: Array<string | undefined>) {
  return values.filter((value): value is string => value !== undefined).join(" ");
}
