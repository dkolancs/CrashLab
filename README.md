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

CrashLab needs a local Docker daemon and permission to run a helper container with `NET_ADMIN` on the target network namespace. The helper image is `crashlab-netem:local`. It uses `tc netem` when the kernel provides it. Kernels built without `sch_netem` instead redirect the target port to a delay proxy in that same network namespace.
