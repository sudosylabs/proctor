import { describe, expect, it } from "vitest";

import { formatMinutes, groupSetupKey, minutesUntil } from "./presentation";
import { signedInName } from "./SignedInName";

describe("account security presentation", () => {
  it("groups a setup key in fours without dropping a short final group", () => {
    expect(groupSetupKey("JBSWY3DPEHPK3PXP")).toBe("JBSW Y3DP EHPK 3PXP");
    expect(groupSetupKey("JBSWY3DPEH")).toBe("JBSW Y3DP EH");
  });

  it("states a deadline as whole minutes left, rounded up and never negative", () => {
    const now = Date.parse("2026-11-07T10:00:00Z");
    expect(minutesUntil("2026-11-07T10:10:00Z", now)).toBe(10);
    expect(minutesUntil("2026-11-07T10:09:01Z", now)).toBe(10);
    expect(minutesUntil("2026-11-07T10:00:30Z", now)).toBe(1);
    expect(minutesUntil("2026-11-07T09:59:00Z", now)).toBe(0);
  });

  it("formats minute durations with localized plural units", () => {
    expect(formatMinutes(15)).toBe("15 minutes");
    expect(formatMinutes(1)).toBe("1 minute");
  });

  it("prefers the display name, falls back to the username, and rejects unusable values", () => {
    expect(signedInName({ display_name: " Ada Lovelace ", username: "ada" })).toBe("Ada Lovelace");
    expect(signedInName({ display_name: "", username: "ada" })).toBe("ada");
    expect(signedInName({ display_name: "x".repeat(257), username: "" })).toBeUndefined();
    expect(signedInName(null)).toBeUndefined();
  });
});
