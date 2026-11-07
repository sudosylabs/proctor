import { message } from "../../i18n/messages";
import { AccessTaskIntro } from "../AccessTaskIntro/AccessTaskIntro";

export function SetupContext() {
  return (
    <AccessTaskIntro
      kind={message("webapp.setup.context.eyebrow")}
      heading={message("webapp.setup.context.heading")}
      headingID="setup-heading"
      body={message("webapp.setup.context.body")}
    />
  );
}
