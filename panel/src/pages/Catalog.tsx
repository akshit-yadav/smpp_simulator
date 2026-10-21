import { useState } from "react";
import { catalogApi, keys, useCatalogMutation, useCommandStatuses, useDlrErrors } from "../api/queries";
import type { CommandStatus, DlrError, DlrStatus } from "../api/types";
import { useToast } from "../components/toast";
import { Badge, Card, ConfirmButton, ErrorBox, Field, Loading, Modal, PageHead, Tabs } from "../components/ui";

type Tab = "errors" | "statuses" | "dlr";

const DLR_STATES: [DlrStatus, number, string][] = [
  ["ENROUTE", 1, "Intermediate: in transit"],
  ["DELIVRD", 2, "Delivered to the handset"],
  ["EXPIRED", 3, "Validity period expired"],
  ["DELETED", 4, "Deleted"],
  ["UNDELIV", 5, "Undeliverable"],
  ["ACCEPTD", 6, "Intermediate: accepted"],
  ["UNKNOWN", 7, "Unknown state"],
  ["REJECTD", 8, "Rejected"],
];

function StatusForm({ initial, onClose }: { initial?: CommandStatus; onClose: () => void }) {
  const [code, setCode] = useState(initial ? initial.code.toString(16).toUpperCase() : "400");
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const toast = useToast();
  const m = useCatalogMutation(initial ? catalogApi.updateStatus : catalogApi.createStatus, keys.commandStatuses);
  const value = parseInt(code, 16);
  const inRange = value >= 0x400 && value <= 0x4ff;
  return (
    <Modal
      title={initial ? `Edit 0x${code}` : "Add vendor command status"}
      onClose={onClose}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="btn primary"
            disabled={!inRange || !name || m.isPending}
            onClick={() =>
              m.mutate(
                { code: value, name, description },
                {
                  onSuccess: () => {
                    toast(`Saved 0x${code}`);
                    onClose();
                  },
                },
              )
            }
          >
            Save
          </button>
        </>
      }
    >
      <div className="stack">
        <Field label="Code (hex)" hint="Vendor codes only: 0x400 – 0x4FF." error={!inRange ? "Must be between 400 and 4FF." : undefined}>
          {(id) => (
            <div className="row" style={{ flexWrap: "nowrap", gap: 6 }}>
              <span className="mono">0x</span>
              <input id={id} className="input mono" value={code} disabled={!!initial} maxLength={3} onChange={(e) => setCode(e.target.value.replace(/[^0-9a-f]/gi, "").toUpperCase())} />
            </div>
          )}
        </Field>
        <Field label="Name">{(id) => <input id={id} className="input mono" value={name} maxLength={64} onChange={(e) => setName(e.target.value)} placeholder="VENDOR_BLOCKED" />}</Field>
        <Field label="Description">{(id) => <input id={id} className="input" value={description} maxLength={255} onChange={(e) => setDescription(e.target.value)} />}</Field>
        <ErrorBox error={m.error} />
      </div>
    </Modal>
  );
}

const EMPTY_ERROR: Omit<DlrError, "system"> = {
  code: "",
  label: "",
  suggested_status: "UNDELIV",
  network_error_type: null,
  network_error_code: null,
  dlr_text: null,
};

function ErrorForm({ initial, onClose }: { initial?: DlrError; onClose: () => void }) {
  const [f, setF] = useState<Omit<DlrError, "system">>(initial ?? EMPTY_ERROR);
  const toast = useToast();
  const m = useCatalogMutation(initial ? catalogApi.updateError : catalogApi.createError, keys.dlrErrors);
  const tlv = f.network_error_type !== null;
  const valid = /^[A-Za-z0-9]{1,3}$/.test(f.code) && f.label.trim() && (!tlv || f.network_error_code !== null);
  return (
    <Modal
      title={initial ? `Edit ${initial.code}` : "Add custom DLR error"}
      onClose={onClose}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="btn primary"
            disabled={!valid || m.isPending}
            onClick={() =>
              m.mutate(f, {
                onSuccess: () => {
                  toast(`Saved ${f.code}`);
                  onClose();
                },
              })
            }
          >
            Save
          </button>
        </>
      }
    >
      <div className="form-grid">
        <Field label="Code" hint="Up to 3 letters or digits; appears as err:XXX.">
          {(id) => <input id={id} className="input mono" value={f.code} disabled={!!initial} maxLength={3} onChange={(e) => setF({ ...f, code: e.target.value.replace(/[^A-Za-z0-9]/g, "") })} />}
        </Field>
        <Field label="Suggested DLR status">
          {(id) => (
            <select id={id} className="input" value={f.suggested_status} onChange={(e) => setF({ ...f, suggested_status: e.target.value as DlrStatus })}>
              {DLR_STATES.map(([s]) => (
                <option key={s}>{s}</option>
              ))}
            </select>
          )}
        </Field>
        <Field label="Label" className="span-2">
          {(id) => <input id={id} className="input" value={f.label} maxLength={128} onChange={(e) => setF({ ...f, label: e.target.value })} placeholder="Template Mismatch" />}
        </Field>
        <Field label="DLR text (optional)" hint="Replaces text: in the DLR." className="span-2">
          {(id) => <input id={id} className="input" value={f.dlr_text ?? ""} maxLength={64} onChange={(e) => setF({ ...f, dlr_text: e.target.value || null })} />}
        </Field>
        <div className="span-2">
          <label className="check">
            <input
              type="checkbox"
              checked={tlv}
              onChange={(e) => setF({ ...f, network_error_type: e.target.checked ? 3 : null, network_error_code: e.target.checked ? (f.network_error_code ?? 0) : null })}
            />
            Send a network_error_code TLV
          </label>
        </div>
        {tlv && (
          <>
            <Field label="Network type" hint="3 = GSM">
              {(id) => <input id={id} className="input" type="number" min={0} max={255} value={f.network_error_type ?? 3} onChange={(e) => setF({ ...f, network_error_type: Number(e.target.value) })} />}
            </Field>
            <Field label="Error code">
              {(id) => <input id={id} className="input" type="number" min={0} max={65535} value={f.network_error_code ?? 0} onChange={(e) => setF({ ...f, network_error_code: Number(e.target.value) })} />}
            </Field>
          </>
        )}
        <div className="span-2">
          <ErrorBox error={m.error} />
        </div>
      </div>
    </Modal>
  );
}

export default function Catalog() {
  const [tab, setTab] = useState<Tab>("errors");
  const statuses = useCommandStatuses();
  const errors = useDlrErrors();
  const delStatus = useCatalogMutation(catalogApi.deleteStatus, keys.commandStatuses);
  const delError = useCatalogMutation(catalogApi.deleteError, keys.dlrErrors);
  const [editStatus, setEditStatus] = useState<CommandStatus | "new" | null>(null);
  const [editError, setEditError] = useState<DlrError | "new" | null>(null);

  return (
    <>
      <PageHead title="Error catalog" sub="Codes used by outcome profiles. System codes are read-only; you can add your own." />
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { value: "errors", label: "DLR errors (err:)" },
          { value: "statuses", label: "Command statuses" },
          { value: "dlr", label: "DLR statuses" },
        ]}
      />

      {tab === "errors" && (
        <Card
          title="DLR error codes"
          actions={
            <button type="button" className="btn primary sm" onClick={() => setEditError("new")}>
              Add custom code
            </button>
          }
        >
          {errors.isLoading ? (
            <Loading />
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Code</th>
                    <th>Label</th>
                    <th>Suggested status</th>
                    <th>network_error_code</th>
                    <th>DLR text</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {errors.data?.map((e) => (
                    <tr key={e.code}>
                      <td className="mono">{e.code}</td>
                      <td>
                        {e.label} {e.system ? <Badge plain>System</Badge> : <Badge plain>Custom</Badge>}
                      </td>
                      <td className="mono">{e.suggested_status}</td>
                      <td className="mono">{e.network_error_type !== null ? `type ${e.network_error_type}, code ${e.network_error_code}` : "—"}</td>
                      <td>{e.dlr_text ?? <span className="muted">—</span>}</td>
                      <td className="num">
                        {!e.system && (
                          <span className="row" style={{ justifyContent: "flex-end", gap: 6 }}>
                            <button type="button" className="btn sm" onClick={() => setEditError(e)}>
                              Edit
                            </button>
                            <ConfirmButton small danger label="Delete" title={`Delete ${e.code}?`} message="Codes used by an outcome profile cannot be deleted." onConfirm={() => delError.mutateAsync(e.code)} />
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {tab === "statuses" && (
        <Card
          title="SMPP command_status"
          actions={
            <button type="button" className="btn primary sm" onClick={() => setEditStatus("new")}>
              Add vendor code
            </button>
          }
        >
          {statuses.isLoading ? (
            <Loading />
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Code</th>
                    <th>Name</th>
                    <th>Description</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {statuses.data?.map((s) => (
                    <tr key={s.code}>
                      <td className="mono">{s.hex}</td>
                      <td className="mono">
                        {s.name} {!s.system && <Badge plain>Vendor</Badge>}
                      </td>
                      <td>{s.description}</td>
                      <td className="num">
                        {!s.system && (
                          <span className="row" style={{ justifyContent: "flex-end", gap: 6 }}>
                            <button type="button" className="btn sm" onClick={() => setEditStatus(s)}>
                              Edit
                            </button>
                            <ConfirmButton small danger label="Delete" title={`Delete ${s.hex}?`} message="Codes used by an outcome profile cannot be deleted." onConfirm={() => delStatus.mutateAsync(s.code)} />
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {tab === "dlr" && (
        <Card title="DLR statuses">
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>stat:</th>
                  <th className="num">message_state TLV</th>
                  <th>Meaning</th>
                </tr>
              </thead>
              <tbody>
                {DLR_STATES.map(([s, n, meaning]) => (
                  <tr key={s}>
                    <td className="mono">{s}</td>
                    <td className="num">{n}</td>
                    <td>{meaning}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {editStatus && <StatusForm initial={editStatus === "new" ? undefined : editStatus} onClose={() => setEditStatus(null)} />}
      {editError && <ErrorForm initial={editError === "new" ? undefined : editError} onClose={() => setEditError(null)} />}
    </>
  );
}
