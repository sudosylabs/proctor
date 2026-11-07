import { useAsyncResource } from "../../app/AsyncResource";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { SetupContent } from "../../components/Setup/SetupContent";
import { SetupContext } from "../../components/Setup/SetupContext";
import { message } from "../../i18n/messages";
import styles from "./SetupPage.module.css";
import {
  requestInstallationStatus,
  type SetupStatus,
  submitInstallation,
} from "./SetupApi";

export function SetupPage() {
  const statusResource = useAsyncResource<SetupStatus>(
    requestInstallationStatus,
    { kind: "loading" },
  );
  const status: SetupStatus = statusResource.loading
    ? { kind: "loading" }
    : statusResource.value;

  return (
    <AccessPageShell skipLabel={message("webapp.setup.skip_to_main")}>
      <div className={styles.page}>
        <SetupContext />
        <SetupContent
          status={status}
          submitSetup={submitInstallation}
          onComplete={() => statusResource.replace({ kind: "complete" })}
          onRetry={statusResource.retry}
        />
      </div>
    </AccessPageShell>
  );
}
