import type { ReactNode } from "react";

import { TaskHeading, TaskStatusLine, type TaskStatus } from "../TaskState/TaskState";
import styles from "./AccessTaskIntro.module.css";

export interface AccessTaskIntroProps {
  body: ReactNode;
  focusHeading?: boolean;
  heading: ReactNode;
  headingID: string;
  /**
   * Information needed before acting, shown under the lede only below `48rem`
   * where the verification rail follows the task. The matching rail item
   * carries `beforeTask`.
   */
  beforeTaskNote?: ReactNode;
  /** Present only when the page reports a real outcome. */
  status?: TaskStatus;
}

export function AccessTaskIntro({
  beforeTaskNote,
  body,
  focusHeading = false,
  heading,
  headingID,
  status,
}: AccessTaskIntroProps) {
  return (
    <header className={styles.intro}>
      {status === undefined ? null : <TaskStatusLine status={status} />}
      <TaskHeading focus={focusHeading} id={headingID}>
        {heading}
      </TaskHeading>
      <p className={styles.body}>{body}</p>
      {beforeTaskNote === undefined ? null : (
        <p className={styles.beforeTask} data-proctor-before-task-note="">
          {beforeTaskNote}
        </p>
      )}
    </header>
  );
}
