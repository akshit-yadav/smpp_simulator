import { Link, useNavigate } from "react-router-dom";
import { useDefaults, useProfiles } from "../api/queries";
import { Badge, Card, Empty, ErrorBox, Loading, PageHead } from "../components/ui";
import { fmtAgo } from "../lib/format";

export default function Profiles() {
  const q = useProfiles();
  const defaults = useDefaults();
  const navigate = useNavigate();
  return (
    <>
      <PageHead
        title="Outcome profiles"
        sub="Reusable outcome mixes. A gateway uses the default, a shared profile, or its own custom one."
        actions={
          <Link className="btn primary" to="/profiles/new">
            New profile
          </Link>
        }
      />
      <Card>
        <ErrorBox error={q.error} />
        {q.isLoading ? (
          <Loading />
        ) : !q.data?.length ? (
          <Empty title="No profiles" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Mode</th>
                  <th className="num">Outcomes</th>
                  <th>Description</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {q.data.map((p) => (
                  <tr key={p.id} className="clickable" onClick={() => navigate(`/profiles/${p.id}`)}>
                    <td>
                      <Link to={`/profiles/${p.id}`} onClick={(e) => e.stopPropagation()}>
                        {p.name}
                      </Link>{" "}
                      {defaults.data?.outcome_profile_id === p.id && <Badge plain>Default</Badge>}
                    </td>
                    <td>
                      {p.mode}
                      {p.mode !== "fixed" && <span className="muted"> · {p.distribution}</span>}
                    </td>
                    <td className="num">{p.entries.length}</td>
                    <td className="muted">{p.description}</td>
                    <td className="muted">{fmtAgo(p.updated_at)}</td>
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
