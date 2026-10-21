import { PageHead } from "../components/ui";

export default function ComingSoon({ title, milestone, what }: { title: string; milestone: string; what: string }) {
  return (
    <>
      <PageHead title={title} />
      <div className="card empty">
        <h2>Arrives in milestone {milestone}</h2>
        <p style={{ maxWidth: 520, margin: "0 auto" }}>{what}</p>
      </div>
    </>
  );
}
