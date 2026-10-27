import { Link, useNavigate } from "react-router-dom";
import { useDefaults, useGateways, useRules } from "../api/queries";
import { Badge, Card, Empty, ErrorBox, Loading, PageHead, Term } from "../components/ui";
import { fmtAgo } from "../lib/format";

export default function DlrRules() {
  const q = useRules();
  const defaults = useDefaults();
  const gateways = useGateways();
  const navigate = useNavigate();
  const usedBy = (id: number) => (gateways.data ?? []).filter((g) => g.effective.dlr_rule_id === id).length;

  return (
    <>
      <PageHead
        title="DLR Rules"
        sub="Reusable rules that decide each message's result. Every gateway uses one, chosen on its Connection tab."
        actions={
          <Link className="btn primary" to="/dlr-rules/new">
            New DLR rule
          </Link>
        }
      />
      <Card>
        <ErrorBox error={q.error} />
        {q.isLoading ? (
          <Loading />
        ) : !q.data?.length ? (
          <Empty title="No DLR rules" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>
                    <Term info="mode">Mode</Term>
                  </th>
                  <th className="num">Entries</th>
                  <th className="num">Gateways</th>
                  <th>Description</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {q.data.map((r) => (
                  <tr key={r.id} className="clickable" onClick={() => navigate(`/dlr-rules/${r.id}`)}>
                    <td>
                      <Link to={`/dlr-rules/${r.id}`} onClick={(e) => e.stopPropagation()}>
                        {r.name}
                      </Link>{" "}
                      {defaults.data?.dlr_rule_id === r.id && <Badge plain>Default</Badge>}
                    </td>
                    <td>
                      {r.mode}
                      {r.mode !== "fixed" && <span className="muted"> · {r.distribution}</span>}
                    </td>
                    <td className="num">{r.entries.length}</td>
                    <td className="num">{usedBy(r.id)}</td>
                    <td className="muted">{r.description}</td>
                    <td className="muted">{fmtAgo(r.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}
