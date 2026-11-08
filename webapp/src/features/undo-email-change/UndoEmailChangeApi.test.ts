import { afterEach, describe, expect, it, vi } from "vitest";

import { apiClient } from "../../api/client";
import { apiResult } from "../../test/ApiClientTestAdapter";
import {
  inspectEmailChangeUndo,
  resolveEmailChangeUndoResult,
  undoEmailChange,
} from "./UndoEmailChangeApi";

describe("resolveEmailChangeUndoResult", () => {
  it("accepts only the declared no-content success", () => {
    expect(resolveEmailChangeUndoResult({ status: 204 }, undefined)).toEqual({
      kind: "restored",
    });
    expect(resolveEmailChangeUndoResult({ status: 200 }, undefined)).toEqual({
      kind: "unavailable",
    });
  });

  it.each([
    ["authentication.account_token.invalid", "invalid"],
    ["request.invalid", "invalid"],
    ["authentication.email_change_undo.conflict", "conflict"],
    ["authentication.rate_limited", "rate_limited"],
    ["authentication.account_recovery.unavailable", "unavailable"],
    ["unexpected.code", "unavailable"],
  ])("maps %s to %s", (code, kind) => {
    expect(
      resolveEmailChangeUndoResult(
        { status: 400 },
        { type: "/problems/x", title: "Failed", status: 400, code },
      ),
    ).toEqual({ kind });
  });
});

describe("undoEmailChange", () => {
  afterEach(() => vi.restoreAllMocks());

  it("sends only the captured token", async () => {
    const post = vi
      .spyOn(apiClient, "POST")
      .mockResolvedValue(apiResult(204));
    await expect(undoEmailChange("token-value")).resolves.toEqual({
      kind: "restored",
    });
    expect(post).toHaveBeenCalledWith("/api/v1/auth/email-change/undo", {
      body: { token: "token-value" },
    });
  });

  it("fails safely when the request rejects", async () => {
    vi.spyOn(apiClient, "POST").mockRejectedValue(new Error("offline"));
    await expect(undoEmailChange("token-value")).resolves.toEqual({
      kind: "unavailable",
    });
  });
});

describe("inspectEmailChangeUndo", () => {
  afterEach(() => vi.restoreAllMocks());

  const facts = { restored_email: "ada.old@example.edu", masked_current_email: "a***@example.org", changed_at: "2026-08-18T10:00:00Z" };

  it("posts only the token and names the change", async () => {
    const post = vi.spyOn(apiClient, "POST").mockResolvedValue(apiResult(200, { data: facts }));
    await expect(inspectEmailChangeUndo("fragment-token")).resolves.toEqual({
      kind: "known", restoredEmail: "ada.old@example.edu", maskedCurrentEmail: "a***@example.org", changedAt: "2026-08-18T10:00:00Z",
    });
    expect(post).toHaveBeenCalledWith("/api/v1/auth/email-change/undo/inspect", { body: { token: "fragment-token" } });
  });

  it("treats failures and malformed bodies as unknown", async () => {
    const post = vi.spyOn(apiClient, "POST");
    for (const result of [
      apiResult(200, { data: { ...facts, restored_email: "nope" } }),
      apiResult(200, { data: { ...facts, masked_current_email: "ada.new@example.org" } }),
      apiResult(200, { data: { ...facts, changed_at: "yesterday" } }),
      apiResult(400, { problemCode: "authentication.account_token.invalid" }),
    ]) {
      post.mockResolvedValue(result);
      await expect(inspectEmailChangeUndo("fragment-token")).resolves.toEqual({ kind: "unknown" });
    }
    post.mockRejectedValue(new Error("offline"));
    await expect(inspectEmailChangeUndo("fragment-token")).resolves.toEqual({ kind: "unknown" });
  });
});
