import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useDeleteRule, useRule, useSaveRule } from "../api/queries";
import type { DlrRuleInput } from "../api/types";
import DlrRuleEditor, { forSave, ignoredEntries, newRule, ruleProblems } from "../components/DlrRuleEditor";
import { useToast } from "../components/toast";
import { Card, ConfirmButton, ErrorBox, Field, Loading, Modal, PageHead } from "../components/ui";

export default function DlrRuleEdit() {
  const { id } = useParams();
  const editing = id !== undefined;
  const q = useRule(editing ? Number(id) : 0);
  const save = useSaveRule(editing ? Number(id) : undefined);
  const remove = useDeleteRule();
  const navigate = useNavigate();
  const toast = useToast();
  const [form, setForm] = useState<DlrRuleInput | null>(editing ? null : newRule());
  const [error, setError] = useState<unknown>(null);
  const [confirmTrim, setConfirmTrim] = useState(false);

  useEffect(() => {
    if (editing && q.data && !form) {
      const { name, description, mode, distribution, seed, entries } = q.data;
      setForm({ name, description, mode, distribution, seed, entries });
    }
  }, [editing, q.data, form]);

  if (!form) return q.error ? <ErrorBox error={q.error} /> : <Loading />;
  const problems = [...(form.name.trim() ? [] : ["Name is required."]), ...ruleProblems(form)];
  const ignored = ignoredEntries(form);

  const doSave = async () => {
    setConfirmTrim(false);
    setError(null);
    try {
      const r = await save.mutateAsync(forSave({ ...form, name: form.name.trim() }));
      toast(editing ? `Saved ${r.name}; gateways using it pick it up within a second` : `Created ${r.name}`);
      navigate("/dlr-rules");
    } catch (err) {
      setError(err);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    // A Fixed rule keeps only its first entry: confirm before dropping the others.
    if (ignored > 0) setConfirmTrim(true);
    else void doSave();
  };

  return (
    <form onSubmit={submit}>
      <PageHead
        title={editing ? `Edit ${q.data?.name}` : "New DLR rule"}
        sub="A DLR rule decides each message's result: the submit_sm_resp status and, for accepted messages, the DLR status and error."
        actions={
          <>
            <Link className="btn" to="/dlr-rules">
              Cancel
            </Link>
            {editing && (
              <ConfirmButton
                danger
                label="Delete"
                title={`Delete ${q.data?.name}?`}
                message="A DLR rule that is used by a gateway or by Gateway Defaults cannot be deleted."
                confirmLabel="Delete rule"
                onConfirm={async () => {
                  await remove.mutateAsync(Number(id));
                  toast("DLR rule deleted");
                  navigate("/dlr-rules");
                }}
              />
            )}
            <button className="btn primary" disabled={save.isPending || problems.length > 0} title={problems.join(" ")}>
              {save.isPending ? "Saving…" : "Save rule"}
            </button>
          </>
        }
      />
      <div className="stack">
        <ErrorBox error={error} />
        <Card>
          <div className="form-grid">
            <Field label="Name">
              {(fid) => <input id={fid} className="input" value={form.name} maxLength={128} onChange={(e) => setForm({ ...form, name: e.target.value })} required />}
            </Field>
            <Field label="Description">
              {(fid) => <input id={fid} className="input" value={form.description} maxLength={255} onChange={(e) => setForm({ ...form, description: e.target.value })} />}
            </Field>
          </div>
        </Card>
        <Card title="Entries">
          <DlrRuleEditor value={form} onChange={setForm} />
        </Card>
        {problems.length > 0 && (
          <div className="alert warning">
            {problems.map((p) => (
              <div key={p}>{p}</div>
            ))}
          </div>
        )}
      </div>
      {confirmTrim && (
        <Modal
          title="Save as a Fixed rule?"
          onClose={() => setConfirmTrim(false)}
          actions={
            <>
              <button type="button" className="btn" onClick={() => setConfirmTrim(false)}>
                Cancel
              </button>
              <button type="button" className="btn primary" onClick={doSave}>
                Remove {ignored} and save
              </button>
            </>
          }
        >
          <p style={{ margin: 0 }}>
            Fixed mode uses only the first entry, “{form.entries[0].name || "Entry 1"}”. Saving removes the other {ignored} {ignored === 1 ? "entry" : "entries"}{" "}
            from this rule.
          </p>
        </Modal>
      )}
    </form>
  );
}
