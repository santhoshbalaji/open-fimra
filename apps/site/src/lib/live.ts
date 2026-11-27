import snapshot from "../../public/data/live.json";

type Snapshot = {
  version: number;
  updatedAt: string | null;
  start: string | null;
  latestPass: string | null;
  latestTreePass: string | null;
  count: number | null;
  cells: { count: number; vertices: number[][] }[];
};

export const live: Snapshot = snapshot;
export const snapshotLabel = (date: string) => new Intl.DateTimeFormat("en-IN", {
  timeZone: "Asia/Kolkata", dateStyle: "medium", timeStyle: "short", hourCycle: "h23",
}).format(new Date(date)) + " IST";
