import { Button } from "@multica/ui/components/ui/button";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { DragStrip } from "@multica/views/platform";

export function LocalWorkbenchRecoveryPage({
  onRetry,
  isRetrying = false,
}: {
  onRetry: () => void;
  isRetrying?: boolean;
}) {
  return (
    <div className="flex h-screen flex-col">
      <DragStrip />
      <div className="flex flex-1 items-center justify-center p-8">
        <div className="flex max-w-sm flex-col items-center text-center">
          <MulticaIcon bordered size="lg" />
          <h1 className="mt-6 text-title font-semibold">
            Local workbench unavailable
          </h1>
          <p className="mt-2 text-body text-muted-foreground">
            Multica could not reach its local service. Check the local runtime and try again.
          </p>
          <Button className="mt-6" disabled={isRetrying} onClick={onRetry}>
            {isRetrying ? "Retrying…" : "Retry"}
          </Button>
        </div>
      </div>
    </div>
  );
}
