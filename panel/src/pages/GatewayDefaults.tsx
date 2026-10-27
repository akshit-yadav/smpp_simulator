import { useEffect, useState, type FormEvent } from "react";
import { useDefaults, useRules, useSaveDefaults } from "../api/queries";
import type { Defaults, Settings, SettingKey } from "../api/types";
import { DlrFields, LimitFields } from "../components/SettingsForm";
import { useToast } from "../components/toast";
import { Card, ErrorBox, Field, Loading, PageHead } from "../components/ui";

export default function GatewayDefaults() {
  const q = useDefaults();
  const rules = useRules();
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
        toast("Gateway Defaults saved; gateways that follow them update within a second");
      },
    });
  };

  return (
    <form onSubmit={submit}>
      <PageHead
        title="Gateway Defaults"
        sub="Gateways follow these settings unless a setting is changed on the gateway itself."
        actions={
          <button className="btn primary" disabled={save.isPending || invalid}>
            {save.isPending ? "Saving…" : "Save defaults"}
          </button>
        }
      />
      <div className="stack">
        <ErrorBox error={save.error} />
        <Card title="DLR Rule">
          <Field label="Default DLR Rule" info="dlrRule">
            {(id) => (
              <select id={id} className="input" style={{ maxWidth: 360 }} value={form.dlr_rule_id} onChange={(e) => set("dlr_rule_id", Number(e.target.value))}>
                {rules.data?.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name} ({r.mode})
                  </option>
                ))}
              </select>
            )}
          </Field>
        </Card>
        <Card title="DLR Settings">
          <DlrFields values={form} defaults={form} showCustom={false} onChange={set} />
        </Card>
        <Card title="Limits">
          <LimitFields values={form} defaults={form} showCustom={false} onChange={set} />
        </Card>
      </div>
    </form>
  );
}
