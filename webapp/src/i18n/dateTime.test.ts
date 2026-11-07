import { describe, expect, it } from "vitest";

import { formatDateTime } from "./dateTime";

describe("formatDateTime", () => {
  it("names the weekday, date, time, and zone in the requested locale", () => {
    expect(
      formatDateTime("2026-11-14T09:42:00Z", "en-GB", "Africa/Lagos"),
    ).toMatch(/^Saturday,? 14 November 2026,? (at )?10:42 (WAT|GMT\+1)$/);
  });

  it("converts the instant into the viewer's time zone", () => {
    expect(
      formatDateTime("2026-11-14T23:30:00Z", "en-US", "Asia/Tokyo"),
    ).toMatch(/^Sunday, November 15, 2026,? (at )?8:30\sAM GMT\+9$/);
  });
});
