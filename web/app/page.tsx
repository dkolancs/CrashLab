"use client";

import { useCallback, useEffect, useMemo, useState } from "react";

const API = process.env.NEXT_PUBLIC_CRASHLAB_API ?? "http://127.0.0.1:8080";

type Service = {
  name: string;
  dependsOn: string[];
  health: string;
  status: string;
  ports: string[];
  restartCount: number;
};

type Experiment = {
  id: number;
  name: string;
  status: string;
  phase: string;
  target: string;
  delayMs: number;
  baselineMs: number;
  durationMs: number;
  recoveryMs: number;
  entryUrl: string;
  entryService: string;
  startedAt: string;
  finishedAt?: string;
  error?: string;
};

type EventRow = { id: number; at: string; kind: string; service: string; message: string };
type Bucket = { t: string; p95: number; errorRate: number; successCount?: number };
type Window = { p50: number; p95: number; errorRate: number; count: number; successCount?: number };
type Hop = { service: string; at: string; reason: string };
type Assertion = { name: string; pass: boolean; detail: string };
type Result = {
  baseline: Window;
  failure: Window;
  recovery: Window;
  recoveryMs: number | null;
  degraded: boolean;
  conclusion: string;
  propagation: Hop[];
  assertions?: Assertion[];
  retryFactor: number;
  text: string;
};
type HistoryRun = { id: number; recoveryMs: number | null; details?: string[] };
type NameHistory = {
  name: string;
  judged: number;
  passed: number;
  passText: string;
  trend: string;
  recovery: HistoryRun[];
  failed: HistoryRun[];
  knownBad: HistoryRun[];
};
type CompareDelta = {
  sameName: boolean;
  leftName: string;
  rightName: string;
  errorRateDelta: number;
  recoveryDeltaMs: number | null;
  leftRecoveryMs: number | null;
  rightRecoveryMs: number | null;
};

type Snapshot = {
  experiment: Experiment | null;
  topology: { project: string; services: Service[] };
  events: EventRow[];
  nodeStates: Record<string, string>;
  buckets: Bucket[];
  result: Result | null;
  recent: Experiment[];
  warning?: string;
};

const empty: Snapshot = {
  experiment: null,
  topology: { project: "", services: [] },
  events: [],
  nodeStates: {},
  buckets: [],
  result: null,
  recent: [],
};

export default function Page() {
  const [snap, setSnap] = useState<Snapshot>(empty);
  const [target, setTarget] = useState("payment-service");
  const [faultType, setFaultType] = useState("latency");
  const [delayMs, setDelayMs] = useState(800);
  const [percent, setPercent] = useState(20);
  const [suggestions, setSuggestions] = useState<Array<{ service: string; reasons: string[]; faults: string[] }>>([]);
  const [compareLeft, setCompareLeft] = useState("");
  const [compareRight, setCompareRight] = useState("");
  const [comparison, setComparison] = useState<{ left: Snapshot; right: Snapshot; delta?: CompareDelta } | null>(null);
  const [history, setHistory] = useState<NameHistory[]>([]);
  const [baselineS, setBaselineS] = useState(15);
  const [durationS, setDurationS] = useState(30);
  const [recoveryS, setRecoveryS] = useState(15);
  const [entryUrl, setEntryUrl] = useState("http://127.0.0.1:8081/checkout");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");

  const load = useCallback(async (id?: number) => {
    const url = id ? `${API}/api/experiments/${id}` : `${API}/api/experiments`;
    const res = await fetch(url);
    if (!res.ok) {
      const body = await res.json().catch(() => ({ error: res.statusText }));
      throw new Error(body.error ?? res.statusText);
    }
    const data = (await res.json()) as Snapshot;
    setSnap(data);
    return data;
  }, []);

  useEffect(() => {
    load().catch((err: Error) => setFormError(err.message));
    fetch(`${API}/api/suggestions`)
      .then((res) => (res.ok ? res.json() : { suggestions: [] }))
      .then((body) => setSuggestions(body.suggestions ?? []))
      .catch(() => undefined);
  }, [load]);

  useEffect(() => {
    fetch(`${API}/api/history`)
      .then((res) => (res.ok ? res.json() : { history: [] }))
      .then((body) => setHistory(body.history ?? []))
      .catch(() => undefined);
  }, [snap.experiment?.id, snap.experiment?.status]);

  const running = snap.experiment?.status === "running";
  const experimentId = snap.experiment?.id;

  useEffect(() => {
    if (!running || !experimentId) return;
    const source = new EventSource(`${API}/api/experiments/${experimentId}/stream`);
    source.onmessage = (event) => {
      setSnap(JSON.parse(event.data) as Snapshot);
    };
    source.onerror = () => {
      source.close();
      load(experimentId).catch(() => undefined);
    };
    return () => source.close();
  }, [running, experimentId, load]);

  async function startExperiment() {
    setBusy(true);
    setFormError("");
    try {
      const res = await fetch(`${API}/api/experiments`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: `${target}-${faultType}`,
          target,
          faultType,
          delayMs,
          lossPercent: percent,
          cpuPercent: faultType === "cpu" ? percent : 0,
          baselineMs: baselineS * 1000,
          durationMs: durationS * 1000,
          recoveryMs: recoveryS * 1000,
          entryUrl,
          entryService: "storefront",
        }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.error ?? "could not start");
      await load(body.id);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "could not start");
    } finally {
      setBusy(false);
    }
  }

  const services = snap.topology.services ?? [];
  const phase = snap.experiment ? `${snap.experiment.status} · ${snap.experiment.phase}` : "idle";
  const faultVisible = !!snap.experiment && !["setup", "baseline"].includes(snap.experiment.phase);

  return (
    <main className="app">
      <header>
        <div>
          <div className="mark">CrashLab</div>
          <h1>{snap.experiment?.name ?? "Latency experiment"}</h1>
        </div>
        <div className="status">
          <span className={`dot ${snap.experiment?.status ?? ""}`} />
          {phase}
        </div>
      </header>
      <div className="layout">
        <section className="stack">
          <div className="panel">
            <h2>Experiment</h2>
            {formError ? <p className="err">{formError}</p> : null}
            {snap.warning ? <p className="warn">{snap.warning}</p> : null}
            <div className="form">
              <label className="wide">
                Target
                <select value={target} onChange={(e) => setTarget(e.target.value)}>
                  {(services.length ? services : [{ name: "payment-service" }]).map((svc) => (
                    <option key={svc.name} value={svc.name}>{svc.name}</option>
                  ))}
                </select>
              </label>
              <label>
                Fault
                <select value={faultType} onChange={(e) => setFaultType(e.target.value)}>
                  <option value="latency">latency</option>
                  <option value="loss">packet loss</option>
                  <option value="kill">terminate</option>
                  <option value="cpu">cpu stress</option>
                </select>
              </label>
              {faultType === "latency" ? (
                <label>
                  Delay (ms)
                  <input type="number" min={1} value={delayMs} onChange={(e) => setDelayMs(Number(e.target.value))} />
                </label>
              ) : null}
              {faultType === "loss" || faultType === "cpu" ? (
                <label>
                  Percent
                  <input type="number" min={1} max={100} value={percent} onChange={(e) => setPercent(Number(e.target.value))} />
                </label>
              ) : null}
              <label>
                Baseline (s)
                <input type="number" min={1} value={baselineS} onChange={(e) => setBaselineS(Number(e.target.value))} />
              </label>
              <label>
                Fault (s)
                <input type="number" min={1} value={durationS} onChange={(e) => setDurationS(Number(e.target.value))} />
              </label>
              <label>
                Recovery (s)
                <input type="number" min={1} value={recoveryS} onChange={(e) => setRecoveryS(Number(e.target.value))} />
              </label>
              <label className="wide">
                Entry URL
                <input value={entryUrl} onChange={(e) => setEntryUrl(e.target.value)} />
              </label>
            </div>
            <button disabled={busy || running} onClick={startExperiment}>
              {running ? "Running" : "Run experiment"}
            </button>
            {running && snap.experiment ? (
              <button
                onClick={() => {
                  fetch(`${API}/api/experiments/${snap.experiment?.id}/stop`, { method: "POST" }).catch((err: Error) => setFormError(err.message));
                }}
              >
                Stop
              </button>
            ) : null}
            {suggestions.length ? (
              <div className="recent">
                {suggestions.map((item) => (
                  <button key={item.service} onClick={() => { setTarget(item.service); setFaultType(item.faults[0] ?? "latency"); }}>
                    suggest {item.service}
                  </button>
                ))}
              </div>
            ) : null}
            {snap.recent?.length ? (
              <div className="recent">
                {snap.recent.map((exp) => (
                  <button key={exp.id} onClick={() => load(exp.id).catch((err: Error) => setFormError(err.message))}>
                    #{String(exp.id).padStart(3, "0")} {exp.status}
                  </button>
                ))}
              </div>
            ) : null}
          </div>
          <div className="panel">
            <h2>Compare runs</h2>
            <div className="form">
              <label>
                Left
                <select value={compareLeft} onChange={(e) => setCompareLeft(e.target.value)}>
                  <option value="">choose</option>
                  {(snap.recent ?? []).map((exp) => (
                    <option key={exp.id} value={exp.id}>#{exp.id} {exp.name}</option>
                  ))}
                </select>
              </label>
              <label>
                Right
                <select value={compareRight} onChange={(e) => setCompareRight(e.target.value)}>
                  <option value="">choose</option>
                  {(snap.recent ?? []).map((exp) => (
                    <option key={exp.id} value={exp.id}>#{exp.id} {exp.name}</option>
                  ))}
                </select>
              </label>
            </div>
            <button
              disabled={!compareLeft || !compareRight}
              onClick={() => {
                fetch(`${API}/api/compare?left=${compareLeft}&right=${compareRight}`)
                  .then((res) => res.json())
                  .then((body) => setComparison(body))
                  .catch((err: Error) => setFormError(err.message));
              }}
            >
              Compare
            </button>
            {comparison?.left?.result && comparison?.right?.result ? (
              <CompareRuns left={comparison.left} right={comparison.right} delta={comparison.delta} />
            ) : null}
          </div>
          <div className="panel">
            <h2>History</h2>
            <HistoryList items={history} />
          </div>
          <div className="panel">
            <h2>Service graph</h2>
            <ServiceGraph services={services} states={snap.nodeStates ?? {}} fault={faultVisible ? snap.experiment?.target : undefined} />
          </div>
        </section>
        <section className="stack">
          <div className="panel">
            <h2>p95 latency</h2>
            <LatencyChart
              buckets={snap.buckets ?? []}
              events={snap.events ?? []}
              startedAt={snap.experiment?.startedAt}
            />
          </div>
          <div className="panel">
            <h2>Event timeline</h2>
            <Timeline events={snap.events ?? []} startedAt={snap.experiment?.startedAt} />
          </div>
          <div className="panel">
            <h2>Report</h2>
            {snap.experiment?.error ? <p className="err">{snap.experiment.error}</p> : null}
            <Report result={snap.result} />
          </div>
        </section>
      </div>
    </main>
  );
}

function ServiceGraph({ services, states, fault }: { services: Service[]; states: Record<string, string>; fault?: string }) {
  const layout = useMemo(() => place(services), [services]);
  if (!services.length) {
    return <p className="warn">No running Compose services. Start the demo with docker compose up.</p>;
  }
  const width = 360;
  const height = Math.max(layout.rows, 1) * 92 + 16;
  return (
    <svg className="graph" viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Service dependency graph">
      <defs>
        <marker id="arrow" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6" fill="#8ea394" />
        </marker>
      </defs>
      {services.flatMap((svc) =>
        (svc.dependsOn ?? []).map((dep) => {
          const from = layout.pos.get(svc.name);
          const to = layout.pos.get(dep);
          if (!from || !to) return null;
          return (
            <line
              key={`${svc.name}-${dep}`}
              x1={from.x}
              y1={from.y + 24}
              x2={to.x}
              y2={to.y - 24}
              stroke="#8ea394"
              strokeWidth="1.2"
              markerEnd="url(#arrow)"
            />
          );
        }),
      )}
      {services.map((svc) => {
        const pos = layout.pos.get(svc.name);
        if (!pos) return null;
        const state = states[svc.name] ?? (svc.name === fault ? "fault" : "healthy");
        const color = state === "fault" ? "#ff5a36" : state === "degraded" ? "#e6a23c" : "#8fce7a";
        return (
          <g key={svc.name} transform={`translate(${pos.x - 78}, ${pos.y - 24})`}>
            <rect width="156" height="48" rx="6" fill="#10140f" stroke={color} strokeWidth="1.5" />
            <text className="node-name" x="78" y="20" textAnchor="middle" fill="#e4efe4">{svc.name}</text>
            <text className="node-state" x="78" y="36" textAnchor="middle" fill={color}>{state}</text>
          </g>
        );
      })}
    </svg>
  );
}

function place(services: Service[]) {
  const memo = new Map<string, number>();
  const rankOf = (name: string, seen: Set<string>): number => {
    const cached = memo.get(name);
    if (cached !== undefined) return cached;
    if (seen.has(name)) return 0;
    seen.add(name);
    const parents = services.filter((svc) => (svc.dependsOn ?? []).includes(name)).map((svc) => svc.name);
    const rank = parents.length ? 1 + Math.max(...parents.map((parent) => rankOf(parent, seen))) : 0;
    seen.delete(name);
    memo.set(name, rank);
    return rank;
  };
  const groups = new Map<number, string[]>();
  let rows = 1;
  for (const svc of services) {
    const rank = rankOf(svc.name, new Set());
    rows = Math.max(rows, rank + 1);
    const list = groups.get(rank) ?? [];
    list.push(svc.name);
    groups.set(rank, list);
  }
  const pos = new Map<string, { x: number; y: number }>();
  for (const [rank, names] of groups) {
    names.sort();
    names.forEach((name, index) => {
      const x = ((index + 1) / (names.length + 1)) * 360;
      pos.set(name, { x, y: 36 + rank * 92 });
    });
  }
  return { pos, rows };
}

function LatencyChart({ buckets, events, startedAt }: { buckets: Bucket[]; events: EventRow[]; startedAt?: string }) {
  if (!buckets.length || !startedAt) {
    return <p className="warn">Latency appears once the load generator records requests.</p>;
  }
  const start = Date.parse(startedAt);
  const points = buckets.filter((bucket) => bucket.successCount !== 0).map((bucket) => ({
    x: (Date.parse(bucket.t) - start) / 1000,
    y: bucket.p95,
  }));
  const maxX = Math.max(1, ...points.map((p) => p.x));
  const maxY = Math.max(1, ...points.map((p) => p.y));
  const w = 640;
  const h = 180;
  const pad = 28;
  const sx = (x: number) => pad + (x / maxX) * (w - pad * 2);
  const sy = (y: number) => h - pad - (y / maxY) * (h - pad * 2);
  const d = points.map((p, i) => `${i === 0 ? "M" : "L"}${sx(p.x).toFixed(1)},${sy(p.y).toFixed(1)}`).join(" ");
  const marker = (kind: string) => events.find((event) => event.kind === kind);
  const injected = marker("fault_injected");
  const removed = marker("fault_removed");
  const line = (event?: EventRow) => {
    if (!event) return null;
    const x = sx((Date.parse(event.at) - start) / 1000);
    return <line x1={x} x2={x} y1={pad} y2={h - pad} stroke="#ff5a36" strokeDasharray="3 3" />;
  };
  return (
    <svg className="chart" viewBox={`0 0 ${w} ${h}`} role="img" aria-label="p95 latency over the experiment">
      <line x1={pad} x2={w - pad} y1={h - pad} y2={h - pad} stroke="#2c3a30" />
      <path d={d} fill="none" stroke="#d6ff4a" strokeWidth="2" />
      {line(injected)}
      {line(removed)}
      <text className="axis" x={pad} y={16}>{Math.round(maxY)} ms</text>
      <text className="axis" x={w - pad - 36} y={h - 8}>{Math.round(maxX)}s</text>
    </svg>
  );
}

function Timeline({ events, startedAt }: { events: EventRow[]; startedAt?: string }) {
  if (!events.length || !startedAt) return <p className="warn">Events show up as the fault is injected and removed.</p>;
  const start = Date.parse(startedAt);
  return (
    <ol className="timeline">
      {events.map((event) => {
        const seconds = Math.max(0, Math.round((Date.parse(event.at) - start) / 1000));
        const label = `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
        return (
          <li key={event.id || `${event.kind}-${event.at}`}>
            <time>{label}</time>
            <span>{event.message}</span>
          </li>
        );
      })}
    </ol>
  );
}

function HistoryList({ items }: { items: NameHistory[] }) {
  if (!items.length) return <p className="warn">Completed runs show a pass rate and recovery times here.</p>;
  return (
    <div>
      {items.map((item) => {
        const recovery = item.recovery ?? [];
        const failed = item.failed ?? [];
        const knownBad = item.knownBad ?? [];
        return (
          <article key={item.name}>
            <h3>{item.name}</h3>
            {item.judged === 0 && knownBad.length ? <p>known bad, not in the pass rate</p> : <p>pass rate {item.passText}</p>}
            {item.judged > 0 && knownBad.length ? <p>known bad, not in the pass rate</p> : null}
            {recovery.map((point) => (
              <p key={point.id}>recovery #{String(point.id).padStart(3, "0")} {point.recoveryMs === null ? "not observed" : `${(point.recoveryMs / 1000).toFixed(1)}s`}</p>
            ))}
            <p>{item.trend}</p>
            {failed.flatMap((run) => (run.details?.length ? run.details : ["assertion failed"]).map((detail) => (
              <p key={`${run.id}-${detail}`}>#{String(run.id).padStart(3, "0")} {detail}</p>
            )))}
            {knownBad.flatMap((run) => (run.details?.length ? run.details : ["assertion failed"]).map((detail) => (
              <p key={`bad-${run.id}-${detail}`}>#{String(run.id).padStart(3, "0")} {detail}</p>
            )))}
          </article>
        );
      })}
    </div>
  );
}

function CompareRuns({ left, right, delta }: { left: Snapshot; right: Snapshot; delta?: CompareDelta }) {
  const leftResult = left.result;
  const rightResult = right.result;
  if (!leftResult || !rightResult) return null;
  const same = delta?.sameName ?? left.experiment?.name === right.experiment?.name;
  return (
    <div>
      <p>{same ? "Same experiment name." : "These runs use different experiment names."}</p>
      {delta ? (
        <p>
          Error rate delta {(delta.errorRateDelta * 100).toFixed(1)} points. Recovery delta{" "}
          {delta.recoveryDeltaMs === null ? "n/a" : `${(delta.recoveryDeltaMs / 1000).toFixed(1)}s`}.
        </p>
      ) : null}
      <h3>#{left.experiment?.id} {left.experiment?.name}</h3>
      <RunSummary result={leftResult} />
      <h3>#{right.experiment?.id} {right.experiment?.name}</h3>
      <RunSummary result={rightResult} />
    </div>
  );
}

function RunSummary({ result }: { result: Result }) {
  return (
    <>
      <div className="numbers">
        <WindowCard title="Baseline" window={result.baseline} />
        <WindowCard title="During failure" window={result.failure} />
        <WindowCard title="Recovery" window={result.recovery} />
      </div>
      <p>Recovery time: {result.recoveryMs === null ? "n/a" : `${(result.recoveryMs / 1000).toFixed(1)}s`}</p>
      <p>Assertions: {assertionLine(result.assertions)}</p>
      <p>Propagation: {(result.propagation ?? []).map((hop) => hop.service).join(" → ") || "none"}</p>
    </>
  );
}

function assertionLine(assertions?: Assertion[]) {
  if (!assertions?.length) return "none";
  return assertions.map((item) => `${item.pass ? "PASS" : "FAIL"} ${item.detail}`).join("; ");
}

function Report({ result }: { result: Result | null }) {
  if (!result) return <p className="warn">The baseline, failure, and recovery comparison appears when the run finishes.</p>;
  return (
    <>
      <div className="numbers">
        <WindowCard title="Baseline" window={result.baseline} />
        <WindowCard title="During failure" window={result.failure} />
        <WindowCard title="Recovery" window={result.recovery} />
      </div>
      <p className="conclusion">{result.conclusion}</p>
      {result.recoveryMs !== null ? <p>Recovery time: {(result.recoveryMs / 1000).toFixed(1)} seconds</p> : null}
      {result.retryFactor >= 2 ? <p>Target receive rate: {result.retryFactor.toFixed(1)}x baseline</p> : null}
      <pre className="report">{result.text}</pre>
    </>
  );
}

function WindowCard({ title, window }: { title: string; window?: Window }) {
  if (!window) return null;
  return (
    <article>
      <h3>{title}</h3>
      <p>p50 {window.successCount === 0 ? "n/a" : fmtMs(window.p50)}</p>
      <p>p95 {window.successCount === 0 ? "n/a" : fmtMs(window.p95)}</p>
      <p>errors {(window.errorRate * 100).toFixed(1)}%</p>
    </article>
  );
}

function fmtMs(ms: number) {
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)} sec`;
  return `${Math.round(ms)} ms`;
}
