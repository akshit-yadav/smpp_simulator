import { useState } from "react";
import { useDeleteWorker, useUpdateWorker, useWorkers } from "../api/queries";
import type { Worker } from "../api/types";
import { useToast } from "../components/toast";
import { Badge, Card, ConfirmButton, Empty, ErrorBox, Field, Loading, Modal, PageHead, errorMessage } from "../components/ui";
import { fmtAgo, fmtTime } from "../lib/format";

function Rename({ worker, onClose }: { worker: Worker; onClose: () => void }) {
  const [name, setName] = useState(worker.name);
  const update = useUpdateWorker();
  return (
    <Modal
      title={`Rename ${worker.name}`}
      onClose={onClose}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={!name.trim() || update.isPending} onClick={() => update.mutate({ id: worker.id, name: name.trim() }, { onSuccess: onClose })}>
            Save
          </button>
        </>
      }
    >
      <Field label="Name">{(id) => <input id={id} className="input" value={name} maxLength={64} onChange={(e) => setName(e.target.value)} autoFocus />}</Field>
      <ErrorBox error={update.error} />
    </Modal>
  );
}

export default function Workers() {
  const q = useWorkers();
  const update = useUpdateWorker();
  const remove = useDeleteWorker();
  const toast = useToast();
  const [renaming, setRenaming] = useState<Worker | null>(null);

  return (
    <>
      <PageHead title="Workers" sub="Each host runs one psg-worker process with many SMPP listeners. Workers register themselves." />
      <Card>
        <ErrorBox error={q.error} />
        {q.isLoading ? (
          <Loading />
        ) : !q.data?.length ? (
          <Empty title="No workers yet">
            <p>Start psg-worker with PSG_CONTROL_URL and PSG_INTERNAL_TOKEN set; it appears here within 5 seconds.</p>
          </Empty>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th className="num">ID</th>
                  <th>Name</th>
                  <th>IP</th>
                  <th>State</th>
                  <th>Port range</th>
                  <th className="num">Gateways</th>
                  <th>Version</th>
                  <th>Last seen</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {q.data.map((w) => (
                  <tr key={w.id}>
                    <td className="num">{w.id}</td>
                    <td>{w.name}</td>
                    <td className="mono">{w.ip}</td>
                    <td>
                      <span className="row" style={{ gap: 6 }}>
                        {w.online ? <Badge tone="good">Online</Badge> : <Badge tone="critical">Offline</Badge>}
                        {w.status === "draining" && <Badge tone="warning">Draining</Badge>}
                      </span>
                    </td>
                    <td className="mono">
                      {w.port_min}–{w.port_max}
                      {w.port_exclude.length > 0 && <div className="small muted">excl. {w.port_exclude.join(", ")}</div>}
                    </td>
                    <td className="num">{w.gateways}</td>
                    <td className="mono">{w.version ?? "—"}</td>
                    <td title={fmtTime(w.last_seen)}>{fmtAgo(w.last_seen)}</td>
                    <td className="num">
                      <span className="row" style={{ justifyContent: "flex-end", gap: 6, flexWrap: "nowrap" }}>
                        <button type="button" className="btn sm" onClick={() => setRenaming(w)}>
                          Rename
                        </button>
                        {w.status === "active" ? (
                          <ConfirmButton
                            small
                            label="Drain"
                            title={`Drain ${w.name}?`}
                            message="No new gateways can be placed on a draining worker. Its existing gateways keep running; move them to another worker (edit each gateway) before taking it down."
                            confirmLabel="Drain"
                            onConfirm={async () => {
                              await update.mutateAsync({ id: w.id, status: "draining" });
                              toast(`${w.name} is draining`);
                            }}
                          />
                        ) : (
                          <button
                            type="button"
                            className="btn sm"
                            onClick={() => update.mutate({ id: w.id, status: "active" }, { onSuccess: () => toast(`${w.name} is active`), onError: (e) => toast(errorMessage(e), true) })}
                          >
                            Activate
                          </button>
                        )}
                        <ConfirmButton
                          small
                          danger
                          label="Remove"
                          disabled={w.online || w.gateways > 0}
                          title={`Remove ${w.name}?`}
                          message="The worker is forgotten. If it starts again it registers itself again."
                          onConfirm={() => remove.mutateAsync(w.id)}
                        />
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      {renaming && <Rename worker={renaming} onClose={() => setRenaming(null)} />}
    </>
  );
}
