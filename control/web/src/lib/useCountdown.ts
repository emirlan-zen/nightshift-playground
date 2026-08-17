import { useEffect, useState } from "react";

export interface Countdown {
  label: string;
  done: boolean;
}

function targetTime(target?: Date | string | null) {
  if (!target) return null;
  const time = target instanceof Date ? target.getTime() : new Date(target).getTime();
  return Number.isFinite(time) ? time : null;
}

export function countdownAt(target: Date | string, now: number): Countdown | null {
  const end = targetTime(target);
  if (end === null) return null;

  const remaining = Math.max(0, end - now);
  const totalSeconds = Math.ceil(remaining / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const label = hours
    ? `${hours}h ${String(minutes).padStart(2, "0")}m`
    : `${String(minutes).padStart(2, "0")}m ${String(seconds).padStart(2, "0")}s`;

  return { label, done: remaining === 0 };
}

export function useCountdown(target?: Date | string | null): Countdown | null {
  const [now, setNow] = useState(() => Date.now());
  const end = targetTime(target);

  useEffect(() => {
    if (end === null) return;

    let timer: number | undefined;
    const refresh = () => {
      const current = Date.now();
      setNow(current);
      if (current >= end && timer !== undefined) {
        window.clearInterval(timer);
        timer = undefined;
      }
    };
    const initial = window.setTimeout(refresh, 0);
    if (end <= Date.now()) return () => window.clearTimeout(initial);

    timer = window.setInterval(refresh, 1000);
    return () => {
      window.clearTimeout(initial);
      if (timer !== undefined) window.clearInterval(timer);
    };
  }, [end]);

  return end === null ? null : countdownAt(new Date(end), now);
}

/** One shared clock for screens that render several related countdowns. */
export function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!active) return;
    const initial = window.setTimeout(() => setNow(Date.now()), 0);
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
    };
  }, [active]);

  return now;
}
