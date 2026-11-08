import { useEffect, useState } from "react";

import { apiClient } from "../../api/client";
import { boundedText, isRecord } from "./AccountSecurityApi";

/**
 * Reads the current User's display name for the hosted-shell footer. It
 * returns undefined for any failure; the footer then simply omits the
 * signed-in line. The value is presentation only and never retained beyond
 * component memory.
 */
export async function requestSignedInName(): Promise<string | undefined> {
  try {
    const { data, response } = await apiClient.GET("/api/v1/users/me");
    return response.status === 200 ? signedInName(data) : undefined;
  } catch { return undefined; }
}

export function signedInName(value: unknown): string | undefined {
  if (!isRecord(value)) return undefined;
  if (boundedText(value.display_name, 256)) return value.display_name.trim();
  if (boundedText(value.username, 256)) return value.username.trim();
  return undefined;
}

/**
 * Loads the signed-in name once `enabled` becomes true. Pages enable it only
 * after the security status confirms an ordinary Session, so a recovery-
 * restricted Session never calls ordinary account APIs.
 */
export function useSignedInName(enabled: boolean, load = requestSignedInName): string | undefined {
  const [name, setName] = useState<string>();
  useEffect(() => {
    if (!enabled || name !== undefined) return;
    let current = true;
    void load().then((value) => { if (current) setName(value); });
    return () => { current = false; };
  }, [enabled, load, name]);
  return enabled ? name : undefined;
}
