# opsgenie-exporter

A [Prometheus](https://prometheus.io/) exporter for [OpsGenie](https://www.atlassian.com/software/opsgenie). Exposes on-call schedules, alerts, heartbeats, teams, users, and incidents as Prometheus metrics.

Built on the official [OpsGenie Go SDK v2](https://github.com/opsgenie/opsgenie-go-sdk-v2).

---

## Metrics

### On-call

| Metric | Type | Labels | Description |
|---|---|---|---|
| `opsgenie_oncall_info` | Gauge | `schedule_id`, `schedule_name`, `schedule_timezone`, `user_name`, `user_id`, `user_type` | 1 per participant currently on call per schedule |

This is an info metric (value always 1). Use it with label selectors or joins:

```promql
# Who is on call right now?
opsgenie_oncall_info

# Alert if no one is on call for a schedule
count(opsgenie_oncall_info) by (schedule_name) == 0
```

### Alerts

| Metric | Type | Labels | Description |
|---|---|---|---|
| `opsgenie_alerts_total` | Gauge | `status`, `priority` | Count of active alerts partitioned by status (`open`, `acknowledged`) and priority (`P1`–`P5`) |

### Heartbeats

| Metric | Type | Labels | Description |
|---|---|---|---|
| `opsgenie_heartbeat_enabled` | Gauge | `name`, `owner_team` | 1 if the heartbeat monitor is enabled |
| `opsgenie_heartbeat_expired` | Gauge | `name`, `owner_team` | 1 if the heartbeat has not pinged within its interval |

### Teams

| Metric | Type | Labels | Description |
|---|---|---|---|
| `opsgenie_team_info` | Gauge | `team_id`, `team_name` | Info metric — 1 per team |
| `opsgenie_teams_total` | Gauge | — | Total number of teams |

### Users

| Metric | Type | Labels | Description |
|---|---|---|---|
| `opsgenie_user_info` | Gauge | `user_id`, `username`, `full_name`, `role` | Info metric — 1 per user |
| `opsgenie_users_total` | Gauge | `role` | User count partitioned by role |

### Incidents

| Metric | Type | Labels | Description |
|---|---|---|---|
| `opsgenie_incident_info` | Gauge | `incident_id`, `message`, `status`, `priority`, `owner_team` | Info metric — 1 per open/acknowledged incident |
| `opsgenie_incidents_total` | Gauge | `status`, `priority` | Count of open/acknowledged incidents |

### Scrape metadata

| Metric | Type | Description |
|---|---|---|
| `opsgenie_scrape_duration_seconds` | Gauge | Wall time for the full API scrape |
| `opsgenie_scrape_success` | Gauge | 1 if all sub-collectors completed without errors |

Each sub-collector (alerts, on-call, heartbeats, teams, users, incidents) fails independently — a single API error does not zero-out the other metrics; `opsgenie_scrape_success` is set to 0 only when at least one sub-collector fails.

---

## Requirements

- An OpsGenie API key with read access
- Go 1.26+ (to build from source)

---

## Usage

### Binary

```bash
# API key via flag
opsgenie-exporter --opsgenie.api-key=<YOUR_KEY>

# API key via environment variable (recommended)
export OPSGENIE_API_KEY=<YOUR_KEY>
opsgenie-exporter

# EU tenant
opsgenie-exporter --opsgenie.api-url=api.eu.opsgenie.com
```

All flags:

| Flag | Default | Description |
|---|---|---|
| `--opsgenie.api-key` | — | API key (`OPSGENIE_API_KEY` env var as fallback) |
| `--opsgenie.api-url` | `api.opsgenie.com` | API FQDN — use `api.eu.opsgenie.com` for EU tenants |
| `--opsgenie.scrape-timeout` | `60s` | Timeout for a full API scrape |
| `--web.listen-address` | `:9428` | Address to expose metrics on |
| `--web.telemetry-path` | `/metrics` | Path to expose metrics at |
| `--version` | — | Print version and exit |

Endpoints exposed:

| Path | Description |
|---|---|
| `/metrics` | Prometheus metrics (OpenMetrics format) |
| `/healthz` | Liveness probe — returns `200 ok` |
| `/` | Landing page with link to `/metrics` |

### Docker

```bash
docker run --rm \
  -e OPSGENIE_API_KEY=<YOUR_KEY> \
  -p 9428:9428 \
  ghcr.io/gxben/opsgenie-exporter:latest

# EU tenant
docker run --rm \
  -e OPSGENIE_API_KEY=<YOUR_KEY> \
  -p 9428:9428 \
  ghcr.io/gxben/opsgenie-exporter:latest \
  --opsgenie.api-url=api.eu.opsgenie.com
```

### Helm (Kubernetes)

The chart is published to GHCR as an OCI artifact.

```bash
# Install with a chart-managed Secret
helm install opsgenie-exporter \
  oci://ghcr.io/gxben/charts/opsgenie-exporter \
  --set opsgenie.apiKey=<YOUR_KEY>

# EU tenant
helm install opsgenie-exporter \
  oci://ghcr.io/gxben/charts/opsgenie-exporter \
  --set opsgenie.apiKey=<YOUR_KEY> \
  --set opsgenie.apiURL=api.eu.opsgenie.com

# Use a pre-existing Secret (e.g. managed by External Secrets Operator)
helm install opsgenie-exporter \
  oci://ghcr.io/gxben/charts/opsgenie-exporter \
  --set opsgenie.existingSecret=my-secret \
  --set opsgenie.existingSecretKey=api-key
```

Enable Prometheus Operator integration:

```bash
helm install opsgenie-exporter \
  oci://ghcr.io/gxben/charts/opsgenie-exporter \
  --set opsgenie.apiKey=<YOUR_KEY> \
  --set serviceMonitor.enabled=true \
  --set prometheusRule.enabled=true
```

The chart ships four default alert rules when `prometheusRule.enabled=true`:

| Alert | Severity | Condition |
|---|---|---|
| `OpsGenieExporterDown` | warning | `opsgenie_scrape_success == 0` for 5 min |
| `OpsGenieHeartbeatExpired` | critical | `opsgenie_heartbeat_expired == 1` for 5 min |
| `OpsGenieHighPriorityAlerts` | warning | Any open P1/P2 alert |
| `OpsGenieNoOnCallUser` | critical | Schedule has zero on-call participants for 5 min |

Key chart values:

| Value | Default | Description |
|---|---|---|
| `opsgenie.apiKey` | `""` | API key (stored in a chart-managed Secret) |
| `opsgenie.existingSecret` | `""` | Use a pre-existing Secret instead |
| `opsgenie.apiURL` | `""` | API FQDN; empty = `api.opsgenie.com` (US) |
| `opsgenie.scrapeTimeout` | `"60s"` | Scrape timeout passed to the exporter |
| `serviceMonitor.enabled` | `false` | Create a Prometheus Operator `ServiceMonitor` |
| `prometheusRule.enabled` | `false` | Create a Prometheus Operator `PrometheusRule` |
| `serviceMonitor.interval` | `"120s"` | Prometheus scrape interval |

---

## Development

### Pre-commit hooks

```bash
pip install pre-commit
pre-commit install --hook-type pre-commit --hook-type commit-msg
```

Hooks enforce Conventional Commits format and run `go build`, `go mod tidy`, `go test`, `go fmt`, `golangci-lint`, `gosec`, and `govulncheck` before every push.

### Commit convention

This project uses [Conventional Commits](https://www.conventionalcommits.org/):

| Type | Release |
|---|---|
| `feat` | minor |
| `fix`, `perf`, `refactor`, `revert` | patch |
| `BREAKING CHANGE` footer or `!` suffix | major |
| `docs`, `chore`, `style`, `test`, `ci` | none |

### Make targets

Tools (`golangci-lint`, `govulncheck`, `gosec`) are downloaded on demand into `bin/` — no global installation required.

```bash
make build        # compile to bin/opsgenie-exporter
make run          # build and run (reads OPSGENIE_API_KEY from env)
make test         # run tests with race detector
make fmt / vet / lint / sec / vuln
make docker       # build multi-arch image locally (no push)
make docker-push  # build and push to GHCR
make helm-package # package the Helm chart as a .tgz
make clean        # remove bin/, coverage.out, *.tgz
```

### CI/CD

| Workflow | Trigger | Purpose |
|---|---|---|
| `ci.yml` | push / PR to `main` | Tests, lint, build, Docker image (push on `main` only) |
| `sec.yml` | push / PR to `main` | gosec security scan |
| `vuln.yml` | push / PR to `main` | govulncheck CVE scan |
| `release.yml` | manual `workflow_dispatch` | semantic-release → Docker image → Helm chart → ArtifactHub |

Trigger a release by running the `release.yml` workflow manually. It determines the next version from conventional commits, updates `CHANGELOG.md`, creates a GitHub Release, and publishes the Docker image and Helm chart.

> **Note**: set `RELEASE_TOKEN` in repository secrets (PAT with `contents: write`) so semantic-release can push the CHANGELOG commit back to `main` when branch protection is enabled.

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).
Copyright 2026 Benjamin Zores.
