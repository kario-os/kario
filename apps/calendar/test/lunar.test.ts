// The Chinese calendar against dates everyone can look up: New Years,
// leap months (2033's, the one older programs got wrong), festivals and
// solar terms.
import { expectEqual, test } from "@kario/test";
import { fmtDate } from "../src/dates";
import { cycle, dayNumber, leapMonth, lunarDate, newYear, yearTerms } from "../src/lunar";

test("Chinese New Year, 1990 to 2030", () => {
  const ny: Record<number, string> = {
    1990: "1990-01-27", 1999: "1999-02-16", 2000: "2000-02-05", 2001: "2001-01-24", 2002: "2002-02-12", 2003: "2003-02-01",
    2004: "2004-01-22", 2005: "2005-02-09", 2006: "2006-01-29", 2007: "2007-02-18", 2008: "2008-02-07", 2009: "2009-01-26",
    2010: "2010-02-14", 2011: "2011-02-03", 2012: "2012-01-23", 2013: "2013-02-10", 2014: "2014-01-31", 2015: "2015-02-19",
    2016: "2016-02-08", 2017: "2017-01-28", 2018: "2018-02-16", 2019: "2019-02-05", 2020: "2020-01-25", 2021: "2021-02-12",
    2022: "2022-02-01", 2023: "2023-01-22", 2024: "2024-02-10", 2025: "2025-01-29", 2026: "2026-02-17", 2027: "2027-02-06",
    2028: "2028-01-26", 2029: "2029-02-13", 2030: "2030-02-03",
  };
  for (const [y, d] of Object.entries(ny)) expectEqual(fmtDate(newYear(Number(y))), d, `New Year ${y}`);
});

test("leap months", () => {
  const leap: Record<number, number> = { 2001: 4, 2004: 2, 2006: 7, 2009: 5, 2012: 4, 2014: 9, 2017: 6, 2020: 4, 2023: 2, 2025: 6, 2028: 5, 2031: 3, 2033: 11, 2036: 6 };
  for (let y = 2000; y <= 2036; y++) expectEqual(leapMonth(y), leap[y] ?? 0, `leap month of ${y}`);
});

test("festivals and lunar dates", () => {
  const at = (y: number, m: number, d: number) => lunarDate(dayNumber(y, m, d));
  expectEqual(at(2024, 9, 17), { year: 2024, month: 8, leap: false, day: 15, monthDays: 30 }, "Mid-Autumn 2024");
  expectEqual([at(2025, 10, 6).month, at(2025, 10, 6).day], [8, 15], "Mid-Autumn 2025");
  expectEqual([at(2026, 9, 25).month, at(2026, 9, 25).day], [8, 15], "Mid-Autumn 2026");
  expectEqual([at(2025, 5, 31).month, at(2025, 5, 31).day], [5, 5], "Dragon Boat 2025");
  expectEqual([at(2026, 6, 19).month, at(2026, 6, 19).day], [5, 5], "Dragon Boat 2026");
  const leap6 = at(2025, 7, 25);
  expectEqual([leap6.month, leap6.leap, leap6.day], [6, true, 1], "the first day of 2025's leap 6th month");
  const eve = at(2026, 2, 16);
  expectEqual([eve.year, eve.month, eve.day === eve.monthDays], [2025, 12, true], "New Year's Eve 2026 is the last day of month 12");
  expectEqual(cycle(2025), [1, 5], "2025 is yǐsì, the snake");
  expectEqual(cycle(2026), [2, 6], "2026 is bǐngwǔ, the horse");
});

test("solar terms", () => {
  const terms = yearTerms(2026).map(fmtDate);
  expectEqual(terms[2], "2026-02-04", "Start of Spring 2026");
  expectEqual(terms[5], "2026-03-20", "Spring Equinox 2026");
  expectEqual(terms[6], "2026-04-05", "Clear and Bright 2026");
  expectEqual(terms[11], "2026-06-21", "Summer Solstice 2026");
  expectEqual(terms[20], "2026-11-07", "Start of Winter 2026");
  expectEqual(terms[23], "2026-12-22", "Winter Solstice 2026");
  expectEqual(fmtDate(yearTerms(2025)[6]), "2025-04-04", "Clear and Bright 2025");
  expectEqual(fmtDate(yearTerms(2024)[5]), "2024-03-20", "Spring Equinox 2024");
  expectEqual(fmtDate(yearTerms(2008)[9]), "2008-05-21", "Grain Buds 2008 (a minute past midnight)");
});

test("every day from 1901 to 2099 has a lunar date, in order", () => {
  let prev = lunarDate(dayNumber(1901, 2, 19));
  expectEqual([prev.month, prev.day], [1, 1], "1901's New Year");
  for (let n = dayNumber(1901, 2, 20); n <= dayNumber(2099, 12, 31); n++) {
    const l = lunarDate(n);
    if (l.day !== prev.day + 1 && !(l.day === 1 && prev.day === prev.monthDays)) throw new Error(`day ${fmtDate(n)}: ${JSON.stringify(l)} after ${JSON.stringify(prev)}`);
    if (l.monthDays !== 29 && l.monthDays !== 30) throw new Error(`a month of ${l.monthDays} days`);
    prev = l;
  }
});
