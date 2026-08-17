import type { SVGProps } from "react";
const paths: Record<string, string> = {
  refine: "M8 15l5-5 3 3-5 5",
  plan: "M6 5h12v14H6zM9 9h6M9 13h4",
  implement: "M5 17l7-12 7 12-4-2-3 4-3-4z",
  investigate: "M10 6a5 5 0 110 10 5 5 0 010-10zm4 8l5 5",
  review: "M6 4h12v16H6zM9 9l2 2 4-4",
  amend: "M5 18L18 5l1 3L8 19z",
  validate: "M4 12h4l3 4 8-10",
  preview: "M3 12s4-6 9-6 9 6 9 6-4 6-9 6-9-6-9-6zm9-3a3 3 0 100 6 3 3 0 000-6z",
  integrate: "M5 7h6v4h2V7h6M5 17h6v-4h2v4h6",
  architect: "M5 19L12 4l7 15M8 14h8",
  scout: "M4 15l10-10 5 5-10 10z",
  medic: "M12 20s-8-5-8-10a4 4 0 017-3l1 2 1-2a4 4 0 017 3c0 5-8 10-8 10z",
  scribe: "M5 19l4-1 10-10-3-3L6 15z",
  herald: "M4 10l11-5v14L4 14zM15 10h4",
  wrench: "M14 5a5 5 0 00-5 7L4 17l3 3 5-5a5 5 0 007-5l-3 2-3-3z",
};
export const CUSTOM_ICON_CHOICES = [
  "architect",
  "scout",
  "medic",
  "scribe",
  "herald",
  "wrench",
] as const;
export function NodeIcon({
  name,
  className,
  ...props
}: { name?: string } & SVGProps<SVGSVGElement>) {
  const key = name && paths[name] ? name : "wrench";
  return (
    <svg
      viewBox="0 0 24 24"
      aria-hidden="true"
      className={className}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      <path d={paths[key]} />
    </svg>
  );
}
