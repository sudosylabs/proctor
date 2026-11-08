import { apiClient } from "../../api/client";
import { readProblemValue } from "../../api/problem";
import { isWireTimestamp } from "../../api/timestamp";

export type EmailChangeUndoResult =
  | { kind: "restored" }
  | { kind: "invalid" }
  | { kind: "conflict" }
  | { kind: "rate_limited" }
  | { kind: "unavailable" };

export function resolveEmailChangeUndoResult(
  response: Pick<Response, "status">,
  error: unknown,
): EmailChangeUndoResult {
  if (response.status === 204) {
    return { kind: "restored" };
  }
  switch (readProblemValue(error)?.code) {
    case "authentication.account_token.invalid":
    case "request.invalid":
      return { kind: "invalid" };
    case "authentication.email_change_undo.conflict":
      return { kind: "conflict" };
    case "authentication.rate_limited":
      return { kind: "rate_limited" };
    default:
      return { kind: "unavailable" };
  }
}

export async function undoEmailChange(
  token: string,
): Promise<EmailChangeUndoResult> {
  try {
    const { error, response } = await apiClient.POST(
      "/api/v1/auth/email-change/undo",
      { body: { token } },
    );
    return resolveEmailChangeUndoResult(response, error);
  } catch {
    return { kind: "unavailable" };
  }
}

/** The change an active undo token would reverse, when known. */
export type EmailChangeUndoInspection =
  | {
      kind: "known";
      restoredEmail: string;
      maskedCurrentEmail: string;
      changedAt: string;
    }
  | { kind: "unknown" };

/**
 * Reads, without consuming, the change the fragment token names. Any failure
 * is `unknown`: the explicit undo action stays available and alone decides
 * whether the link still works.
 */
export async function inspectEmailChangeUndo(
  token: string,
): Promise<EmailChangeUndoInspection> {
  try {
    const { data, response } = await apiClient.POST(
      "/api/v1/auth/email-change/undo/inspect",
      { body: { token } },
    );
    if (
      response.status === 200 &&
      typeof data === "object" && data !== null &&
      isMailbox(data.restored_email) &&
      typeof data.masked_current_email === "string" &&
      // The server's masking keeps at most the first character.
      /^([^\s@*]\*{1,3}|\*{3})@[^\s@]+$/u.test(data.masked_current_email) &&
      data.masked_current_email.length <= 254 &&
      isWireTimestamp(data.changed_at)
    ) {
      return {
        kind: "known",
        restoredEmail: data.restored_email,
        maskedCurrentEmail: data.masked_current_email,
        changedAt: data.changed_at,
      };
    }
    return { kind: "unknown" };
  } catch {
    return { kind: "unknown" };
  }
}

function isMailbox(value: unknown): value is string {
  return typeof value === "string" && value.length <= 254 && /^[^\s@]+@[^\s@]+$/.test(value);
}
