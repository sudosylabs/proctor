import {
  forwardRef,
  type HTMLAttributes,
  type ReactNode,
} from "react";

import styles from "./Notice.module.css";

export type NoticeTone =
  | "accent"
  | "information"
  | "success"
  | "warning"
  | "danger";

export interface NoticeProps extends HTMLAttributes<HTMLDivElement> {
  /** Short bold lead that names the point before the supporting text. */
  lead?: ReactNode;
  tone?: NoticeTone;
}

const markedTones: ReadonlySet<NoticeTone> = new Set([
  "success",
  "warning",
  "danger",
]);

export const Notice = forwardRef<HTMLDivElement, NoticeProps>(function Notice(
  { children, className, lead, tone = "information", ...props },
  ref,
) {
  const marked = markedTones.has(tone);
  return (
    <div
      {...props}
      ref={ref}
      className={classes(styles.notice, styles[tone], marked ? styles.marked : undefined, className)}
      data-proctor-notice=""
      data-proctor-notice-tone={tone}
    >
      {marked ? <span className={styles.marker} aria-hidden="true" /> : null}
      <div className={styles.content}>
        {lead === undefined ? null : (
          <>
            <strong className={styles.lead}>{lead}</strong>{" "}
          </>
        )}
        {children}
      </div>
    </div>
  );
});

function classes(...values: Array<string | undefined>) {
  return values.filter((value): value is string => value !== undefined).join(" ");
}
