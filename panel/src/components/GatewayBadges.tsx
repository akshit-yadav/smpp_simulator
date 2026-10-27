import type { Gateway } from "../api/types";
import { Badge } from "./ui";

/** Listener state as the worker reports it. */
export function RuntimeBadge({ gw }: { gw: Gateway }) {
  if (gw.status === "disabled") return <Badge>Disabled</Badge>;
  if (gw.status === "suspended") return <Badge tone="warning">Suspended (licence)</Badge>;
  const rt = gw.runtime;
  if (!rt) return <Badge tone="warning">Waiting for worker</Badge>;
  if (rt.state === "listening") return <Badge tone="good">Listening</Badge>;
  if (rt.state === "error")
    return (
      <span title={rt.error}>
        <Badge tone="critical">Error</Badge>
      </span>
    );
  return <Badge>Stopped</Badge>;
}
