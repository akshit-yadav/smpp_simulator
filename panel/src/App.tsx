import { useEffect, useState } from "react";
import { getReadiness, getWorkers, type Readiness, type Worker } from "./api";

const REFRESH_MS = 5000;

export default function App() {
  const [ready, setReady] = useState<Readiness | null>(null);
  const [workers, setWorkers] = useState<Worker[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const load = async () => {
      try {
        setReady(await getReadiness());
        setWorkers(await getWorkers());
        setError(null);
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    };
    load();
    const id = setInterval(load, REFRESH_MS);
    return () => clearInterval(id);
  }, []);

  return (
    <main>
      <h1>Fake SMPP Gateway</h1>
      <p className="muted">Scaffolding build (M0). Screens arrive in M5.</p>
      {error && <p className="error">{error}</p>}

      <section>
        <h2>Stores</h2>
        <ul>
          {ready &&
            Object.entries(ready.checks).map(([name, state]) => (
              <li key={name}>
                <span className={state === "ok" ? "ok" : "down"}>●</span> {name}: {state}
              </li>
            ))}
        </ul>
      </section>

      <section>
        <h2>Workers</h2>
        {workers.length === 0 ? (
          <p className="muted">No worker heartbeats.</p>
        ) : (
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>IP</th>
                <th>Ports</th>
                <th>Version</th>
                <th>Last heartbeat</th>
              </tr>
            </thead>
            <tbody>
              {workers.map((w) => (
                <tr key={w.worker_id}>
                  <td>{w.worker_id}</td>
                  <td>{w.ip}</td>
                  <td>{w.port_range}</td>
                  <td>{w.version}</td>
                  <td>{new Date(w.ts).toLocaleTimeString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </main>
  );
}
