# Kubernetes Deployment

This guide covers deploying Trident self-hosted on Kubernetes using the official Helm chart.

## Prerequisites

| Requirement | Minimum version | Notes |
|-------------|----------------|-------|
| Kubernetes | 1.25+ | EKS, GKE, AKS, or self-hosted |
| Helm | 3.12+ | `brew install helm` |
| PostgreSQL | 15+ | Operator-managed (e.g. CloudNativePG) or managed service |
| Redis | 7+ | Operator-managed (e.g. Redis Operator) or managed service |

Trident's Helm chart packages the four stateless services only — it does **not** bundle Postgres or Redis. Provision those separately before installing the chart.

## Quick Start

### 1. Add the chart repository (once published)

```bash
helm repo add trident https://telocel-labs.github.io/trident
helm repo update
```

For now, install directly from the cloned repo:

```bash
git clone https://github.com/telocel-labs/trident
cd trident
```

### 2. Create the secrets

Trident uses the `existingSecret` pattern — sensitive values are read from a Kubernetes Secret rather than passed through Helm values.

```bash
kubectl create secret generic trident-secrets \
  --from-literal=DATABASE_URL="postgres://trident:password@postgres-host:5432/trident" \
  --from-literal=REDIS_URL="redis://redis-host:6379" \
  --from-literal=ADMIN_API_KEY="$(openssl rand -hex 32)"
```

### 3. Install the chart

```bash
helm install trident ./helm/trident \
  --namespace trident \
  --create-namespace \
  --set goApi.image.tag=v0.1.0 \
  --set indexer.image.tag=v0.1.0 \
  --set grpcApi.image.tag=v0.1.0
```

### 4. Verify the deployment

```bash
kubectl -n trident get pods
kubectl -n trident get hpa
```

Expected output:

```
NAME                                    READY   STATUS    RESTARTS   AGE
trident-go-api-7d9f8c4b5-abcde         1/1     Running   0          2m
trident-go-api-7d9f8c4b5-fghij         1/1     Running   0          2m
trident-grpc-api-6c8b7d5f4-klmno       1/1     Running   0          2m
trident-indexer-5b4d9c3a2-pqrst        1/1     Running   0          2m
trident-nginx-4a3c8b7d6-uvwxy          1/1     Running   0          2m
```

## Configuration

### Using an Ingress controller instead of Nginx

Disable the bundled Nginx deployment and enable the Ingress resource:

```yaml
# custom-values.yaml
nginx:
  enabled: false

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  host: api.trident.example.com
  tls:
    - secretName: trident-tls
      hosts:
        - api.trident.example.com
```

```bash
helm upgrade trident ./helm/trident -f custom-values.yaml
```

### Horizontal Pod Autoscaler

The Go API scales automatically between 2 and 10 replicas based on CPU utilisation (target: 70%). Configure via:

```yaml
goApi:
  hpa:
    minReplicas: 2
    maxReplicas: 10
    targetCPUUtilizationPercentage: 70
```

Ensure the [Metrics Server](https://github.com/kubernetes-sigs/metrics-server) is installed in your cluster for HPA to function:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

### Resource requests and limits

Adjust per your cluster capacity. Default values are conservative for development:

```yaml
goApi:
  resources:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "256Mi"
```

### Multiple API key support

Create API keys via the admin endpoint after deployment:

```bash
ADMIN_KEY=$(kubectl get secret trident-secrets -o jsonpath='{.data.ADMIN_API_KEY}' | base64 -d)
TRIDENT_HOST="http://$(kubectl get svc trident-nginx -o jsonpath='{.status.loadBalancer.ingress[0].ip}')"

curl -X POST "$TRIDENT_HOST/v1/api-keys" \
  -H "X-Admin-Key: $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"label": "my-app", "network": "mainnet"}'
```

## Health checks

The Go API exposes `GET /v1/health`. Kubernetes liveness and readiness probes are pre-configured in the chart:

- **Liveness** (`failureThreshold: 3`): restarts the container after 3 consecutive failures.
- **Readiness** (`failureThreshold: 1`): removes the pod from the Service load balancer on the first failure for faster traffic isolation.

## Upgrading

```bash
helm upgrade trident ./helm/trident --reuse-values \
  --set goApi.image.tag=v0.2.0 \
  --set indexer.image.tag=v0.2.0 \
  --set grpcApi.image.tag=v0.2.0
```

## Uninstalling

```bash
helm uninstall trident --namespace trident
kubectl delete namespace trident
# Retain the secret if you plan to reinstall:
# kubectl -n trident delete secret trident-secrets
```
