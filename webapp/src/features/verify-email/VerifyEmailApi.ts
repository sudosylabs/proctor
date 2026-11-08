import { apiClient } from "../../api/client";
import { readProblemValue } from "../../api/problem";

export type EmailVerificationResult =
  | { kind: "verified" }
  | { kind: "invalid" }
  | { kind: "unavailable" };

export function resolveEmailVerificationResult(
  response: Pick<Response, "status">,
  error: unknown,
): EmailVerificationResult {
  if (response.status === 204) {
    return { kind: "verified" };
  }

  const code = readProblemValue(error)?.code;
  if (
    code === "authentication.account_token.invalid" ||
    code === "request.invalid"
  ) {
    return { kind: "invalid" };
  }
  return { kind: "unavailable" };
}

export async function completeEmailVerification(
  token: string,
): Promise<EmailVerificationResult> {
  try {
    const { error, response } = await apiClient.POST(
      "/api/v1/auth/email-verification/complete",
      { body: { token } },
    );
    return resolveEmailVerificationResult(response, error);
  } catch {
    return { kind: "unavailable" };
  }
}

/** The address an active verification token would confirm, when known. */
export type EmailVerificationInspection =
  | { kind: "known"; email: string }
  | { kind: "unknown" };

/**
 * Reads, without consuming, the address the fragment token names. Any
 * failure is `unknown`: the page keeps its explicit action either way, and
 * only completion decides whether the link still works.
 */
export async function inspectEmailVerification(
  token: string,
): Promise<EmailVerificationInspection> {
  try {
    const { data, response } = await apiClient.POST(
      "/api/v1/auth/email-verification/inspect",
      { body: { token } },
    );
    return response.status === 200 && isRecord(data) && isMailbox(data.email)
      ? { kind: "known", email: data.email }
      : { kind: "unknown" };
  } catch {
    return { kind: "unknown" };
  }
}

function isMailbox(value: unknown): value is string {
  return typeof value === "string" && value.length <= 254 && /^[^\s@]+@[^\s@]+$/.test(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
