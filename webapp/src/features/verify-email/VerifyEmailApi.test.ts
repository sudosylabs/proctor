import { afterEach, describe, expect, it, vi } from "vitest";

import { apiClient } from "../../api/client";
import { apiResult } from "../../test/ApiClientTestAdapter";
import { inspectEmailVerification, resolveEmailVerificationResult } from "./VerifyEmailApi";

describe("resolveEmailVerificationResult", () => {
  it("accepts only the declared no-content success", () => {
    expect(resolveEmailVerificationResult({ status: 204 }, undefined)).toEqual({
      kind: "verified",
    });
    expect(resolveEmailVerificationResult({ status: 200 }, undefined)).toEqual({
      kind: "unavailable",
    });
  });

  it("maps every concealed terminal token outcome to one invalid state", () => {
    for (const code of [
      "authentication.account_token.invalid",
      "request.invalid",
    ]) {
      expect(
        resolveEmailVerificationResult(
          { status: 400 },
          {
            type: "/problems/email-verification",
            title: "Verification failed",
            status: 400,
            code,
            private_value: "must not escape",
          },
        ),
      ).toEqual({ kind: "invalid" });
    }
  });

  it("keeps transport, rate-limit, and server failures retryable", () => {
    expect(
      resolveEmailVerificationResult(
        { status: 503 },
        {
          type: "/problems/unavailable",
          title: "Unavailable",
          status: 503,
          code: "authentication.account_recovery.unavailable",
        },
      ),
    ).toEqual({ kind: "unavailable" });
  });
});

describe("inspectEmailVerification", () => {
  afterEach(() => vi.restoreAllMocks());

  it("posts only the token and names a valid address", async () => {
    const post = vi.spyOn(apiClient, "POST").mockResolvedValue(apiResult(200, { data: { email: "ada@example.edu" } }));
    await expect(inspectEmailVerification("fragment-token")).resolves.toEqual({ kind: "known", email: "ada@example.edu" });
    expect(post).toHaveBeenCalledWith("/api/v1/auth/email-verification/inspect", { body: { token: "fragment-token" } });
  });

  it("treats failures and malformed bodies as unknown", async () => {
    const post = vi.spyOn(apiClient, "POST");
    for (const result of [
      apiResult(200, { data: { email: "not an address" } }),
      apiResult(200, { data: {} }),
      apiResult(400, { problemCode: "authentication.account_token.invalid" }),
      apiResult(429, { problemCode: "authentication.rate_limited" }),
    ]) {
      post.mockResolvedValue(result);
      await expect(inspectEmailVerification("fragment-token")).resolves.toEqual({ kind: "unknown" });
    }
    post.mockRejectedValue(new Error("offline"));
    await expect(inspectEmailVerification("fragment-token")).resolves.toEqual({ kind: "unknown" });
  });
});
