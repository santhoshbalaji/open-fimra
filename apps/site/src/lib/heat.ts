const ink = ["#b257650e", "#be616426", "#cb69654a", "#d5726878", "#e87f77a8",
  "#f79883de", "#ffb69b", "#ffca9e", "#ffe0ae"];

export const seasonHeat = { ceiling: 500, knee: 5 };
export const weeklyHeat = { ceiling: 30, knee: 1 };

export function heat(count: number, { ceiling, knee }: typeof seasonHeat) {
  const fraction = Math.log1p(Math.max(0, count - 1) / knee) / Math.log1p((ceiling - 1) / knee);
  return ink[Math.min(ink.length - 1, Math.floor(fraction * (ink.length - 1)))];
}
