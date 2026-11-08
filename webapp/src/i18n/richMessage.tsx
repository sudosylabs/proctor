import { Fragment, type ReactNode } from "react";

import { message, type MessageID } from "./messages";

/**
 * Resolves a message whose placeholders are filled with page-owned elements,
 * such as an inline link, while the sentence order stays in the catalog.
 * String values are substituted first; a placeholder without a slot is kept
 * as written so a missing slot is visible rather than silently dropped.
 */
export function richMessage(
  id: MessageID,
  slots: Readonly<Record<string, ReactNode>>,
  values: Readonly<Record<string, string>> = {},
): ReactNode {
  const parts = message(id, values).split(/\{\{\.([A-Za-z]+)\}\}/);
  return parts.map((part, index) => {
    if (index % 2 === 0) {
      return part === "" ? null : <Fragment key={index}>{part}</Fragment>;
    }
    return part in slots ? (
      <Fragment key={index}>{slots[part]}</Fragment>
    ) : (
      <Fragment key={index}>{`{{.${part}}}`}</Fragment>
    );
  });
}
