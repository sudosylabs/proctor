import { useAsyncResource } from "../../app/AsyncResource";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { EmailChangeUndoContent } from "../../components/EmailChangeUndo/EmailChangeUndoContent";
import { message } from "../../i18n/messages";
import {
  inspectEmailChangeUndo,
  undoEmailChange,
  type EmailChangeUndoInspection,
} from "./UndoEmailChangeApi";

export interface UndoEmailChangePageProps {
  token?: string;
}

const unknownChange: EmailChangeUndoInspection = { kind: "unknown" };

export function UndoEmailChangePage({ token }: UndoEmailChangePageProps) {
  // The read-only lookup only describes the change; the explicit undo never
  // waits for it.
  const inspection = useAsyncResource(
    () => (token === undefined ? Promise.resolve(unknownChange) : inspectEmailChangeUndo(token)),
    unknownChange,
    token !== undefined,
  );
  return (
    <AccessPageShell skipLabel={message("webapp.undo_email_change.skip_to_main")}>
      <EmailChangeUndoContent
        change={inspection.value.kind === "known" ? inspection.value : undefined}
        token={token}
        undoEmailChange={undoEmailChange}
      />
    </AccessPageShell>
  );
}
