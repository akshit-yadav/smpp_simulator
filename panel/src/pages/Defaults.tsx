import { useEffect, useState, type FormEvent } from "react";
import { useDefaults, useProfiles, useSaveDefaults } from "../api/queries";
import type { Defaults, Settings, SettingKey } from "../api/types";
import { DlrFields, LimitFields } from "../components/SettingsForm";
import { useToast } from "../components/toast";
import { Card, ErrorBox, Field, Loading, PageHead } from "../components/ui";

export default function DefaultsPage() {
  const q = useDefaults();
  const profiles = useProfiles();
  const save = useSaveDefaults();
  const toast = useToast();
  const [form, setForm] = useState<Defaults | null>(null);

  useEffect(() => {
    if (q.data && !form) setForm(q.data);
  }, [q.data, form]);

  if (!form) return q.error ? <ErrorBox error={q.error} /> : <Loading />;
  const set = <K extends SettingKey>(k: K, v: Settings[K]) => setForm({ ...form, [k]: v } as Defaults);
  const invalid = form.dlr_min_ms > form.dlr_max_ms;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const { updated_at: _ignored, ...body } = form;
    save.mutate(body as Defaults, {
      onSuccess: (d) => {
        setForm(d);
        toast("Defaults saved; gateways that follow them update within a second");
      },
    });
  };

  return (
    <form onSubmit={submit}>
      <PageHead
        title="Defaults"
        sub="Every gateway setting left on “Default” follows these values."
        actions={
          <button className="btn primary" disabled={save.isPending || invalid}>
            {save.isPending ? "Saving…" : "Save defaults"}
          </button>
        }
      />
      <div className="stack">
        <ErrorBox error={save.error} />
        {invalid && <div className="alert warning">Minimum DLR delay is above the maximum.</div>}
        <Card title="Outcomes">
          <Field label="Default outcome profile">
            {(id) => (
              <select id={id} className="input" style={{ maxWidth: 360 }} value={form.outcome_profile_id} onChange={(e) => set("outcome_profile_id", Number(e.target.value))}>
                {profiles.data?.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
        </Card>
        <Card title="Delivery receipts">
          <DlrFields values={form} defaults={form} inherit={false} onChange={set} />
        </Card>
        <Card title="Limits">
          <LimitFields values={form} defaults={form} inherit={false} onChange={set} />
        </Card>
      </div>
    </form>
  );
}
