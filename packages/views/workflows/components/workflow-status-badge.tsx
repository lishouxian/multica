import { Badge } from "@multica/ui/components/ui/badge";
import { useT } from "../../i18n";

// Maps a run status to a badge tone. Unknown statuses render their raw value
// with a neutral tone — a default branch, mirroring how the backend engine
// downgrades unknown routes instead of throwing.
const statusVariant: Record<string, "default" | "secondary" | "destructive" | "outline"> = {
  running: "default",
  paused: "secondary",
  needs_attention: "destructive",
  ejected: "outline",
  done: "secondary",
  cancelled: "outline",
};

export function WorkflowStatusBadge({ status }: { status: string }) {
  const { t } = useT("workflows");
  // Explicit selector per known status; an unknown status renders verbatim (a
  // default branch, mirroring the engine's routing).
  const label = (() => {
    switch (status) {
      case "running":
        return t(($) => $.status.running);
      case "paused":
        return t(($) => $.status.paused);
      case "needs_attention":
        return t(($) => $.status.needs_attention);
      case "ejected":
        return t(($) => $.status.ejected);
      case "done":
        return t(($) => $.status.done);
      case "cancelled":
        return t(($) => $.status.cancelled);
      default:
        return status;
    }
  })();
  return <Badge variant={statusVariant[status] ?? "outline"}>{label}</Badge>;
}
