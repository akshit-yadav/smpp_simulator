import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { reportUrl, useMessageSearch } from "../api/queries";
import type { DlrEvent, SubmitEvent } from "../api/types";
import { Badge, Card, Empty, ErrorBox, Loading, PageHead, Segmented, Term } from "../components/ui";
import { fmtMs, fmtNum, fmtTime, hex } from "../lib/format";

type Kind = "message_id" | "destination";

type Row = { kind: "submit"; e: SubmitEvent } | { kind: "dlr"; e: DlrEvent };

const SUBMIT_TONE = { accepted: "good", rejected: "critical", throttled: "warning", licence_throttled: "warning", invalid: "critical" } as const;
const DLR_TONE = { acknowledged: "good", retried: "warning", failed: "critical", queued: "neutral", expired: "critical" } as const;

export default function MessageSearch() {
  // The search lives in the URL, so a result can be linked and Back works.
  const [params, setParams] = useSearchParams();
  const active: Kind | null = params.get("message_id") ? "message_id" : params.get("destination") ? "destination" : null;
  const [kind, setKind] = useState<Kind>(active ?? "message_id");
  const [text, setText] = useState(active ? (params.get(active) ?? "") : "");
  const search = active ? { [active]: params.get(active) ?? "" } : null;
  const r = useMessageSearch(search);

  const go = (k: Kind, v: string) => {
    setKind(k);
    setText(v);
    setParams(v.trim() ? { [k]: v.trim() } : {});
  };

  const rows: Row[] = [
    ...(r.data?.submits ?? []).map((e) => ({ kind: "submit" as const, e })),
    ...(r.data?.dlrs ?? []).map((e) => ({ kind: "dlr" as const, e })),
  ].sort((a, b) => b.e.ts - a.e.ts || (a.kind === "dlr" ? -1 : 1));

  return (
    <>
      <PageHead title="Message search" sub={<Term info="messageSearch">Every submit_sm and DLR event of a message, from the last 3 days.</Term>} />
      <div className="stack">
        <Card>
          <form
            className="row"
            onSubmit={(e) => {
              e.preventDefault();
              go(kind, text);
            }}
          >
            <Segmented<Kind>
              label="Search by"
              value={kind}
              onChange={setKind}
              options={[
                { value: "message_id", label: "Message ID" },
                { value: "destination", label: "Destination" },
              ]}
            />
            <input
              className="input mono"
              style={{ maxWidth: 320 }}
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={kind === "message_id" ? "e.g. 1a2b3c4d5e6f7" : "e.g. 919812345678"}
              aria-label={kind === "message_id" ? "Message ID" : "Destination address"}
              autoFocus
            />
            <button type="submit" className="btn primary" disabled={!text.trim()}>
              Search
            </button>
            <span className="spacer" />
            {search && (
              <a className="btn" href={reportUrl("/messages", { ...search, format: "csv" })} download>
                Export CSV
              </a>
            )}
          </form>
        </Card>

        {!search ? null : r.isLoading ? (
          <Loading />
        ) : r.error ? (
          <ErrorBox error={r.error} />
        ) : rows.length === 0 ? (
          <div className="card">
            <Empty title="Nothing found">
              <p className="muted">
                No submit_sm or DLR {active === "message_id" ? "with this message ID" : "to this destination"} in the last 3 days.
              </p>
            </Empty>
          </div>
        ) : (
          <>
            {active === "message_id" && r.data?.submits.length === 1 && <Summary s={r.data.submits[0]} dlrs={r.data.dlrs} />}
            <Card title={`${fmtNum(rows.length)} events, newest first`}>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Time</th>
                      <th>Event</th>
                      <th>Message ID</th>
                      <th>Gateway</th>
                      <th>Source</th>
                      <th>Destination</th>
                      <th>Details</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row) => (
                      <tr key={`${row.kind}:${row.e.ts}:${row.e.message_id}:${row.e.result}`}>
                        <td className="nowrap">{fmtTime(row.e.ts)}</td>
                        <td className="nowrap">
                          {row.kind === "submit" ? (
                            <>
                              submit_sm <Badge tone={SUBMIT_TONE[row.e.result]}>{row.e.result.replace("_", " ")}</Badge>
                            </>
                          ) : (
                            <>
                              DLR <Badge tone={DLR_TONE[row.e.result]}>{row.e.result}</Badge>
                            </>
                          )}
                        </td>
                        <td className="mono">
                          {row.e.message_id ? (
                            active === "message_id" ? (
                              row.e.message_id
                            ) : (
                              <button type="button" className="linklike mono" onClick={() => go("message_id", row.e.message_id)}>
                                {row.e.message_id}
                              </button>
                            )
                          ) : (
                            <span className="muted">—</span>
                          )}
                        </td>
                        <td>{row.e.gateway ? <Link to={`/gateways/${row.e.gateway_id}`}>{row.e.gateway}</Link> : <span className="muted">#{row.e.gateway_id}</span>}</td>
                        <td className="mono">{row.e.source_addr || "—"}</td>
                        <td className="mono">{row.e.dest_addr || "—"}</td>
                        <td className="small">{row.kind === "submit" ? submitDetails(row.e) : dlrDetails(row.e)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {r.data?.truncated && <p className="muted small">Showing the newest events only. Search by message ID to see one message in full.</p>}
            </Card>
          </>
        )}
      </div>
    </>
  );
}

function submitDetails(e: SubmitEvent): string {
  const parts: string[] = [];
  if (e.result === "accepted") {
    parts.push(`rule entry "${e.outcome || "(unnamed)"}" → ${e.dlr_status} err ${e.dlr_error}`);
    parts.push(e.registered_delivery & 0x03 ? "DLR requested" : "no DLR requested");
  } else {
    parts.push(`command_status ${hex(e.command_status)}`);
    if (e.outcome) parts.push(`rule entry "${e.outcome}"`);
  }
  if (e.sm_length) parts.push(`${e.sm_length} bytes`);
  return parts.join(" · ");
}

function dlrDetails(e: DlrEvent): string {
  const interim = e.dlr_status === "ENROUTE" || e.dlr_status === "ACCEPTD";
  const parts = [`${interim ? "intermediate " : ""}${e.dlr_status} err ${e.dlr_error}`, `attempt ${e.attempt}`];
  if (e.result === "acknowledged" || e.result === "retried" || e.result === "failed") {
    if (e.delay_ms) parts.push(`sent ${fmtMs(e.delay_ms)} after submit`);
    if (e.resp_ms) parts.push(`answered in ${fmtMs(e.resp_ms)}`);
    else if (e.result !== "acknowledged") parts.push("no answer in time");
    if (e.command_status) parts.push(`deliver_sm_resp ${hex(e.command_status)}`);
  }
  if (e.result === "queued") parts.push("no receiver bound; kept for up to 72 h");
  return parts.join(" · ");
}

function Summary({ s, dlrs }: { s: SubmitEvent; dlrs: DlrEvent[] }) {
  const last = dlrs[0]; // newest first
  return (
    <div className="grid grid-4">
      <div className="tile">
        <div className="tile-label">Message ID</div>
        <div className="tile-value mono small-value">{s.message_id || "—"}</div>
        {s.message_id_dec && <div className="tile-sub mono">decimal {s.message_id_dec}</div>}
      </div>
      <div className="tile">
        <div className="tile-label">submit_sm</div>
        <div className="tile-value small-value">{s.result.replace("_", " ")}</div>
        <div className="tile-sub">{fmtTime(s.ts)}</div>
      </div>
      <div className="tile">
        <div className="tile-label">Route</div>
        <div className="tile-value mono small-value">{s.dest_addr}</div>
        <div className="tile-sub">
          from <span className="mono">{s.source_addr || "—"}</span> via {s.gateway ?? `#${s.gateway_id}`}
        </div>
      </div>
      <div className="tile">
        <div className="tile-label">Last DLR</div>
        <div className="tile-value small-value">{last ? last.result : s.result === "accepted" ? "none yet" : "—"}</div>
        <div className="tile-sub">{last ? `${last.dlr_status} err ${last.dlr_error} · ${fmtTime(last.ts)}` : ""}</div>
      </div>
    </div>
  );
}
