import { useState } from "react";

import { useAsyncResource } from "../../app/AsyncResource";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { SetupContent } from "../../components/Setup/SetupContent";
import { message } from "../../i18n/messages";
import {
  requestInstallationStatus,
  type SetupCreated,
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
  const [created, setCreated] = useState<SetupCreated>();

  return (
    <AccessPageShell skipLabel={message("webapp.setup.skip_to_main")}>
      <SetupContent
        created={created}
        status={status}
        submitSetup={submitInstallation}
        onComplete={(result) => {
          setCreated(result);
          statusResource.replace({ kind: "complete" });
        }}
        onRetry={statusResource.retry}
      />
    </AccessPageShell>
  );
}
