import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useDeleteProfile, useProfile, useSaveProfile } from "../api/queries";
import type { ProfileInput } from "../api/types";
import ProfileEditor, { newProfile, profileProblems } from "../components/ProfileEditor";
import { useToast } from "../components/toast";
import { Card, ConfirmButton, ErrorBox, Field, Loading, PageHead } from "../components/ui";

export default function ProfileEdit() {
  const { id } = useParams();
  const editing = id !== undefined;
  const q = useProfile(editing ? Number(id) : 0);
  const save = useSaveProfile(editing ? Number(id) : undefined);
  const remove = useDeleteProfile();
  const navigate = useNavigate();
  const toast = useToast();
  const [form, setForm] = useState<ProfileInput | null>(editing ? null : newProfile());
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    if (editing && q.data && !form) {
      const { name, description, mode, distribution, seed, entries } = q.data;
      setForm({ name, description, mode, distribution, seed, entries });
    }
  }, [editing, q.data, form]);

  if (!form) return q.error ? <ErrorBox error={q.error} /> : <Loading />;
  const problems = [...(form.name.trim() ? [] : ["Name is required."]), ...profileProblems(form)];

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      const p = await save.mutateAsync({ ...form, name: form.name.trim() });
      toast(editing ? `Saved ${p.name}; gateways using it pick it up within a second` : `Created ${p.name}`);
      navigate("/profiles");
    } catch (err) {
      setError(err);
    }
  };

  return (
    <form onSubmit={submit}>
      <PageHead
        title={editing ? `Edit ${q.data?.name}` : "New outcome profile"}
        sub="Each accepted message gets one outcome: response status, DLR status and DLR error."
        actions={
          <>
            <Link className="btn" to="/profiles">
              Cancel
            </Link>
            {editing && (
              <ConfirmButton
                danger
                label="Delete"
                title={`Delete ${q.data?.name}?`}
                message="A profile that is used by a gateway or by the defaults cannot be deleted."
                confirmLabel="Delete profile"
                onConfirm={async () => {
                  await remove.mutateAsync(Number(id));
                  toast("Profile deleted");
                  navigate("/profiles");
                }}
              />
            )}
            <button className="btn primary" disabled={save.isPending || problems.length > 0} title={problems.join(" ")}>
              {save.isPending ? "Saving…" : "Save profile"}
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
        <Card title="Outcomes">
          <ProfileEditor value={form} onChange={setForm} />
        </Card>
        {problems.length > 0 && (
          <div className="alert warning">
            {problems.map((p) => (
              <div key={p}>{p}</div>
            ))}
          </div>
        )}
      </div>
    </form>
  );
}
