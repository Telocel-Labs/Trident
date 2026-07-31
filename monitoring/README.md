# Trident monitoring

`alerts.yml` is a standard Prometheus rule file. Load it via `rule_files:` in
your Prometheus config:

```yaml
# prometheus.yml
rule_files:
  - "monitoring/alerts.yml"

scrape_configs:
  - job_name: trident-indexer
    static_configs:
      - targets: ["indexer:9090"] # crates/indexer/src/metrics.rs, METRICS_PORT
  - job_name: trident-api
    static_configs:
      - targets: ["api:3000"] # GET /metrics, services/api
```

The `job=` labels above (`trident-indexer`, `trident-api`) must match what
`alerts.yml`'s `up{job="..."}` rules expect — rename both sides together if
you use different job names.

If you run the Prometheus Operator instead of vanilla Prometheus, convert
each `groups:` entry into a `PrometheusRule` CRD's `spec.groups` — the rule
syntax itself (`alert`, `expr`, `for`, `labels`, `annotations`) is unchanged.

## Validating changes

Before committing changes to `alerts.yml`, check it with `promtool` (ships
with Prometheus):

```bash
promtool check rules monitoring/alerts.yml
```

## Metrics catalog and runbook

- The full metrics catalog (every metric `alerts.yml` references, plus
  everything else Trident exports) lives in
  [`docs/metrics-catalog.md`](../docs/metrics-catalog.md).
- Per-alert runbooks (why the threshold, first steps when it fires) live in
  [`docs/runbooks/alerts.md`](../docs/runbooks/alerts.md).
