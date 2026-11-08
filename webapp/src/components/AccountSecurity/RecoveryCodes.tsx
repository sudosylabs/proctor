import { useEffect, useState } from "react";

import { message } from "../../i18n/messages";
import { Button } from "../Button/Button";
import { TaskStateActions } from "../TaskState/TaskState";
import styles from "./AccountSecurity.module.css";

export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone(): void }) {
  const [acknowledged, setAcknowledged] = useState(false);
  const [copyStatus, setCopyStatus] = useState<"copied" | "failed">();

  useEffect(() => {
    function warnBeforeLeaving(event: BeforeUnloadEvent) { event.preventDefault(); event.returnValue = ""; }
    window.addEventListener("beforeunload", warnBeforeLeaving);
    return () => window.removeEventListener("beforeunload", warnBeforeLeaving);
  }, []);

  function download() {
    const url = URL.createObjectURL(new Blob([`${codes.join("\n")}\n`], { type: "text/plain;charset=utf-8" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = "proctor-recovery-codes.txt";
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(`${codes.join("\n")}\n`);
      setCopyStatus("copied");
    } catch { setCopyStatus("failed"); }
  }

  return (
    <div className={styles.stack}>
      <ul className={styles.codes} aria-label={message("webapp.security.codes.list")} translate="no">
        {codes.map((code) => <li key={code}><code>{code}</code></li>)}
      </ul>
      <div className={styles.saveActions}>
        <TaskStateActions>
          <Button variant="secondary" onClick={download}>{message("webapp.security.codes.download")}</Button>
          <Button variant="secondary" onClick={copy}>{message("webapp.security.codes.copy")}</Button>
        </TaskStateActions>
        <p className={styles.hint} role="status" aria-live="polite">
          {copyStatus === undefined ? null : message(copyStatus === "copied" ? "webapp.security.codes.copied" : "webapp.security.codes.copy_failed")}
        </p>
      </div>
      <label className={styles.acknowledgement}>
        <input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.currentTarget.checked)} />
        <span>{message("webapp.security.codes.acknowledge")}</span>
      </label>
      <TaskStateActions>
        <Button disabled={!acknowledged} onClick={onDone}>{message("webapp.security.codes.done")}</Button>
      </TaskStateActions>
    </div>
  );
}
