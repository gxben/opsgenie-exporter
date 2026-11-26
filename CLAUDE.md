# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build        # build binary to bin/opsgenie-exporter
make run          # build + run (requires OPSGENIE_API_KEY env var)
make test         # go test ./... -race -count=1
make lint         # golangci-lint (downloads to bin/ if absent)
make fmt          # go fmt
make vet          # go vet
make sec          # gosec (downloads to bin/ if absent)
make vuln         # govulncheck (downloads to bin/ if absent)
make docker       # multi-arch buildx (no push)
make docker-push  # build + push to GHCR
make helm-package # package charts/opsgenie-exporter
```

Run a single test:
```bash
go test ./internal/collector/... -run TestFoo -v
```

## Architecture

**Entry point** (`main.go`): parses flags, constructs one SDK client per OpsGenie service (`alert`, `schedule`, `heartbeat`, `team`, `user`, `incident`), calls `cfg.ConfigureLogLevel("error")` to suppress SDK logrus noise, then registers the collector with a fresh Prometheus registry and serves `/metrics` + `/healthz`.

**Collector** (`internal/collector/collector.go`): a single `Collector` struct that holds all six SDK clients and every `*prometheus.Desc`. `Run()` polls in the background every `--opsgenie.poll-interval`: `Poll()` runs six sub-collectors in sequence and caches their metrics per sub-collector; `Collect()` only serves that cache and never calls the API. A failed sub-collector keeps its previous metrics (`opsgenie_collector_success` 0, `opsgenie_collector_last_success_timestamp_seconds` unchanged). Poll success/duration meta-metrics are always emitted.

### OpsGenie SDK quirks to keep in mind

- `schedule.ListRequest.Expand` is `*bool` — the SDK dereferences without a nil guard. Always pass `&expandFalse` explicitly.
- `schedule.GetOnCallsRequest.Flat` is `*bool` with the same nil-deref bug. Always pass a pointer. `Flat=false` → response populates `OnCallParticipants []GetOnCallParticipant`; `Flat=true` → response populates `OnCallRecipients []string` (different field, different label richness).
- OpsGenie has no `status:acknowledged` query value. Acknowledged alerts remain `status:open`; use `status:open AND acknowledged:true` to count them.
- `cfg.ConfigureLogLevel("error")` is required — without it the SDK emits logrus `WARN Could not set requestId` on every response.
- API FQDN only (no scheme): `api.opsgenie.com` (US default) or `api.eu.opsgenie.com` (EU). Set via `--opsgenie.api-url` flag or omit for US.
- The SDK retries HTTP 429 four times by default: `collector.RetryPolicy` (set in `main.go`) drops 429 from the retries, and a sub-collector stops at its first 429 (`errThrottled`) until the next poll.
- `client.Exec` calls `req.WithContext(ctx)` without using the result: the context never reaches the HTTP request. Poll timeouts are enforced between calls (`Collector.pause`, `ctx.Err()` checks) and per attempt with `Config.RequestTimeout`.
- `user.ListRequest.ResourcePath()` is `/v2/users/` (trailing slash) — matters for the fake API in `collector_test.go`.

### CI / release pipeline

- **ci.yml**: test → lint → build → docker (push on `main`, not on PRs)
- **release.yml**: manual trigger → semantic-release (CHANGELOG + git tag) → docker (versioned tags + `latest`) → Helm OCI push to `ghcr.io/<owner>/charts` → ArtifactHub metadata push
- **sec.yml** / **vuln.yml**: gosec and govulncheck on push/PR
- Commits must follow Conventional Commits (enforced by pre-commit hook + semantic-release)
- `RELEASE_TOKEN` secret required for semantic-release to push back to `main` through branch protection; falls back to `GITHUB_TOKEN`

### Helm chart

`charts/opsgenie-exporter/` mirrors the deployment exactly. Key values:
- `opsgenie.apiKey` / `opsgenie.existingSecret` — mutually exclusive; secret is checksum-annotated on the deployment
- `opsgenie.apiURL` — empty = US; set to `api.eu.opsgenie.com` for EU tenants
- `serviceMonitor.enabled` / `prometheusRule.enabled` — off by default; require Prometheus Operator CRDs
