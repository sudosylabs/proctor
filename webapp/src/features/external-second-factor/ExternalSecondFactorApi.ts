import { apiClient } from "../../api/client";
import { readProblemValue } from "../../api/problem";
import { isWireTimestamp } from "../../api/timestamp";

/** The paused provider sign-in that the bound browser may describe. */
export interface ExternalSecondFactorContext {
  purpose: "login" | "desktop_authorization";
  /** The account's display name. */
  name: string;
  /** The provider that proved the first factor, as people know it. */
  provider: string;
  expiresAt: string;
}

/** Failures shared by reading and finishing a paused sign-in. */
export type ExternalSecondFactorFailure =
  /** The pause ran out, or no live pause is bound to this browser. */
  | { kind: "expired" }
  /** The account needs a Proctor code, but two-step verification is turned off. */
  | { kind: "mfa_unavailable" }
  | { kind: "unavailable" };

export type ExternalSecondFactorContextResult =
  | { kind: "ready"; context: ExternalSecondFactorContext }
  | ExternalSecondFactorFailure;

export type ExternalSecondFactorCompletion =
  /** A Web sign-in finished; its session cookies are set. */
  | { kind: "signed_in"; returnTo: string }
  /** A Desktop sign-in now waits for the person to allow it. */
  | { kind: "desktop_authenticated" }
  | { kind: "code_invalid" }
  | { kind: "rate_limited" }
  | ExternalSecondFactorFailure;

const path = "/api/v1/auth/external/second-factor";
const fallbackReturn = "/authorization/complete";

// The pause is bound by an HttpOnly cookie the provider callback set. Neither
// call carries a token, and the code is sent once and never kept.
export async function requestExternalSecondFactorContext(): Promise<ExternalSecondFactorContextResult> {
  try {
    const { data, error, response } = await apiClient.GET(path);
    if (response.status !== 200) return failure(readProblemValue(error)?.code);
    if (!validContext(data)) return { kind: "unavailable" };
    return {
      kind: "ready",
      context: {
        purpose: data.purpose,
        name: data.user_display_name,
        provider: data.provider_display_name,
        expiresAt: data.expires_at,
      },
    };
  } catch {
    return { kind: "unavailable" };
  }
}

export async function completeExternalSecondFactor(
  purpose: ExternalSecondFactorContext["purpose"],
  code: string,
): Promise<ExternalSecondFactorCompletion> {
  try {
    const { data, error, response } = await apiClient.POST(path, { body: { code } });
    if (response.status === 200) {
      if (purpose === "desktop_authorization") return { kind: "desktop_authenticated" };
      const body: unknown = data;
      const returnTo = isRecord(body) && typeof body.return_to === "string" ? body.return_to : undefined;
      return { kind: "signed_in", returnTo: safeReturnPath(returnTo) };
    }
    const problem = readProblemValue(error)?.code;
    if (problem === "authentication.mfa.code_invalid") return { kind: "code_invalid" };
    if (problem === "authentication.rate_limited") return { kind: "rate_limited" };
    return failure(problem);
  } catch {
    return { kind: "unavailable" };
  }
}

/**
 * The server chooses where a finished sign-in goes. The page still follows
 * only a same-origin path, never another origin or a protocol-relative URL.
 */
export function safeReturnPath(value: string | undefined): string {
  if (value === undefined || value.length > 2048 || !value.startsWith("/") ||
    value.startsWith("//") || value.includes("\\") || [...value].some((character) => character.charCodeAt(0) < 0x20)) {
    return fallbackReturn;
  }
  try {
    const url = new URL(value, "https://proctor.invalid");
    return url.origin === "https://proctor.invalid" ? `${url.pathname}${url.search}${url.hash}` : fallbackReturn;
  } catch {
    return fallbackReturn;
  }
}

function failure(code: string | undefined): ExternalSecondFactorFailure {
  if (code === "authentication.external.second_factor_expired" || code === "authentication.external.invalid") {
    return { kind: "expired" };
  }
  if (code === "authentication.mfa.unavailable") return { kind: "mfa_unavailable" };
  return { kind: "unavailable" };
}

function validContext(value: unknown): value is {
  purpose: "login" | "desktop_authorization";
  user_display_name: string;
  provider_display_name: string;
  expires_at: string;
} {
  return isRecord(value) &&
    (value.purpose === "login" || value.purpose === "desktop_authorization") &&
    boundedString(value.user_display_name, 256) &&
    boundedString(value.provider_display_name, 128, true) &&
    isWireTimestamp(value.expires_at);
}

function boundedString(value: unknown, maximum: number, required = false): value is string {
  return typeof value === "string" && (!required || value.trim() !== "") && [...value].length <= maximum;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
