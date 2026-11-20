# CrashLab

CrashLab runs a latency experiment against a Docker Compose application, records what changed, and shows how the fault spread across dependent services.

Version 0.1 discovers Compose services, injects egress network latency with `tc netem`, and compares baseline, failure, and recovery. Propagation is timestamp correlation along declared dependencies.

## Demo

```bash
docker compose -f demo/compose.yaml up --build -d
cd web && npm install && cd ..
go run ./cmd/crashlab run --compose demo/compose.yaml
```

Open http://127.0.0.1:3000. The default experiment adds 800ms of egress latency to `payment-service`. Checkout retries with a 400ms timeout, so the storefront slows down and starts failing while inventory stays healthy.

One shot without the dashboard:

```bash
go run ./cmd/crashlab experiment run \
  --target payment-service \
  --delay 800ms \
  --baseline 15s \
  --duration 30s \
  --recovery 15s
```

```bash
go run ./cmd/crashlab report 1
```

Saved experiments live in `experiments/`. `crashlab test` runs every file in a directory and exits non-zero when an assertion fails. A file with `expect.mode: fail` is the opposite: the command exits zero only when an assertion fails.

```bash
go run ./cmd/crashlab experiment run experiments/payment-latency.yaml
go run ./cmd/crashlab test experiments/ci-smoke.yaml
go run ./cmd/crashlab inject loss payment-service --percent 20
go run ./cmd/crashlab inject kill payment-service
go run ./cmd/crashlab inject cpu checkout-service --percent 90
go run ./cmd/crashlab clear payment-service
```

The dashboard compares two saved runs, including baseline, failure, recovery, assertions, propagation, and the error-rate and recovery-time delta. It suggests latency, termination, and packet loss for services that other services depend on, and it leaves datastores off that list unless the entry service health-checks them. Demo services continue one `traceparent` across a checkout, and those edges are included in propagation.

CrashLab needs a local Docker daemon. The Docker socket can control the host, so the API binds to `127.0.0.1:8080` unless `--addr` says otherwise. The helper image is `crashlab-netem:local`. The default helper adds `NET_ADMIN` and uses `tc netem` when the kernel provides it. It does not retry as a privileged container unless `--privileged` or `fault.privileged: true` is set. Kernels built without `sch_netem` instead redirect the target port to a delay proxy in that same network namespace.

`crashlab test` runs the files in a directory one at a time. Each run has a timeout of the baseline, fault, and recovery windows plus 45 seconds, and a timeout still clears the fault. The suite stops on the first cleanup failure. `--shared-baseline` reuses the first run's baseline samples. `--out dir` writes the text report and the JSON outcome. A later run of the same name prints a regression when an assertion fails, the failure error rate rises by at least 5 points, or recovery time rises by at least 2 seconds against the last passing run in that SQLite file. That line records a difference, not a cause.

`crashlab history` groups completed runs by name. The pass rate counts only judged runs: at least 10 baseline and failure samples, and not "Not enough data". A file with `expect.mode: fail` is listed as known bad and is left out of that rate. Recovery times are printed oldest to newest. The dashboard shows the same list.

Apps that already export OTLP HTTP can `POST /v1/traces`. CrashLab keeps server spans whose parent is in the same payload and whose time overlaps the fault window, and turns those into the same observed edges as `/traces`. Apps without traces keep `depends_on`.
