// The words of dates and of the Chinese calendar, English as the source;
// the catalogs give each language's (the lunar ones read in Chinese).
import { tr } from "kario";
import { type Day, weekday, ymd } from "./dates";
import { cycle, dayNumber, lunarDate, termOn } from "./lunar";

export const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
/** A month grid's column heads. */
export const WEEKDAY_HEADS = ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"];
export const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

export const LUNAR_MONTHS = [
  "1st month",
  "2nd month",
  "3rd month",
  "4th month",
  "5th month",
  "6th month",
  "7th month",
  "8th month",
  "9th month",
  "10th month",
  "11th month",
  "12th month",
];

export const LUNAR_DAYS = [
  "1st", "2nd", "3rd", "4th", "5th", "6th", "7th", "8th", "9th", "10th",
  "11th", "12th", "13th", "14th", "15th", "16th", "17th", "18th", "19th", "20th",
  "21st", "22nd", "23rd", "24th", "25th", "26th", "27th", "28th", "29th", "30th",
];

/** The 24 terms in lunar.TERM_DEGREES' order (from Minor Cold). */
export const TERMS = [
  "Minor Cold",
  "Major Cold",
  "Start of Spring",
  "Rain Water",
  "Awakening of Insects",
  "Spring Equinox",
  "Clear and Bright",
  "Grain Rain",
  "Start of Summer",
  "Grain Buds",
  "Grain in Ear",
  "Summer Solstice",
  "Minor Heat",
  "Major Heat",
  "Start of Autumn",
  "End of Heat",
  "White Dew",
  "Autumn Equinox",
  "Cold Dew",
  "Frost's Descent",
  "Start of Winter",
  "Minor Snow",
  "Major Snow",
  "Winter Solstice",
];

export const STEMS = ["Jiǎ", "Yǐ", "Bǐng", "Dīng", "Wù", "Jǐ", "Gēng", "Xīn", "Rén", "Guǐ"];
export const BRANCHES = ["Zǐ", "Chǒu", "Yín", "Mǎo", "Chén", "Sì", "Wǔ", "Wèi", "Shēn", "Yǒu", "Xū", "Hài"];
export const ANIMALS = ["Rat", "Ox", "Tiger", "Rabbit", "Dragon", "Snake", "Horse", "Goat", "Monkey", "Rooster", "Dog", "Pig"];

// The festivals of the Chinese calendar (month, day) and the public
// holidays of the Gregorian one a Chinese calendar names.
const LUNAR_FESTIVALS: [number, number, string][] = [
  [1, 1, "Spring Festival"],
  [1, 15, "Lantern Festival"],
  [5, 5, "Dragon Boat Festival"],
  [7, 7, "Qixi Festival"],
  [7, 15, "Ghost Festival"],
  [8, 15, "Mid-Autumn Festival"],
  [9, 9, "Double Ninth Festival"],
  [12, 8, "Laba Festival"],
];
const SOLAR_FESTIVALS: [number, number, string][] = [
  [1, 1, "New Year's Day"],
  [5, 1, "Labour Day"],
  [10, 1, "National Day"],
];

/** The days the lunar calendar is computed for (checked against an ephemeris). */
export const LUNAR_FIRST = dayNumber(1901, 2, 19);
export const LUNAR_LAST = dayNumber(2099, 12, 31);

export function lunarInRange(d: Day): boolean {
  return d >= LUNAR_FIRST && d <= LUNAR_LAST;
}

/** The lunar month's name: "Leap" before a leap one. */
export function lunarMonthName(month: number, leap: boolean): string {
  const name = tr(LUNAR_MONTHS[month - 1]);
  return leap ? tr("Leap %s", name) : name;
}

/** The festival or term of day d (the Chinese calendar's and China's public holidays), "" for none. */
export function festival(d: Day): string {
  if (!lunarInRange(d)) return "";
  const l = lunarDate(d);
  if (!l.leap) {
    if (l.month === 12 && l.day === l.monthDays) return tr("New Year's Eve");
    for (const [m, dd, name] of LUNAR_FESTIVALS) if (m === l.month && dd === l.day) return tr(name);
  }
  const [, gm, gd] = ymd(d);
  for (const [m, dd, name] of SOLAR_FESTIVALS) if (m === gm && dd === gd) return tr(name);
  const t = termOn(d);
  return t >= 0 ? tr(TERMS[t]) : "";
}

/** What a month grid's day shows under its number: a festival or term, the lunar day, the month's name on its first. */
export function lunarShort(d: Day): string {
  if (!lunarInRange(d)) return "";
  const f = festival(d);
  if (f) return f;
  const l = lunarDate(d);
  return l.day === 1 ? lunarMonthName(l.month, l.leap) : tr(LUNAR_DAYS[l.day - 1]);
}

/** A day's whole lunar date with its year: "the Yǐsì year (Snake), 10th month 28th". */
export function lunarLong(d: Day): string {
  if (!lunarInRange(d)) return "";
  const l = lunarDate(d);
  const [s, b] = cycle(l.year);
  return tr("Lunar %s %s, %s%s year of the %s", lunarMonthName(l.month, l.leap), tr(LUNAR_DAYS[l.day - 1]), tr(STEMS[s]), tr(BRANCHES[b]), tr(ANIMALS[b]));
}

export function weekdayName(d: Day): string {
  return tr(WEEKDAYS[weekday(d)]);
}

export function monthName(m: number): string {
  return tr(MONTHS[m - 1]);
}
