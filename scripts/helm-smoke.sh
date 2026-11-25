#!/bin/sh
# Smoke test of the Helm chart: installs it in a kind cluster with an
# external pgvector database and checks /healthz, /readyz and the worker.
#
#   sh scripts/helm-smoke.sh
#
# KIND_CLUSTER names the cluster, assay-helm by default. The script creates
# the cluster when it does not exist, and deletes it at the end only then.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cluster=${KIND_CLUSTER:-assay-helm}
ctx="kind-$cluster"
ns=assay-smoke
image=assay:helm-smoke
created=0
forward=""

say() { printf 'helm-smoke: %s\n' "$*" >&2; }

cleanup() {
	[ -z "$forward" ] || kill "$forward" 2>/dev/null || true
	if [ "$created" -eq 1 ]; then
		kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
	else
		helm --kube-context "$ctx" uninstall assay -n "$ns" >/dev/null 2>&1 || true
		kubectl --context "$ctx" delete namespace "$ns" --wait=false >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
	say "creating the kind cluster $cluster"
	kind create cluster --name "$cluster" --wait 5m
	created=1
fi

say "building $image"
docker build -q -t "$image" "$root" >/dev/null
kind load docker-image "$image" --name "$cluster"

# The external database: PostgreSQL with pgvector, outside the chart.
kubectl --context "$ctx" create namespace "$ns" --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null
kubectl --context "$ctx" -n "$ns" apply -f - >/dev/null <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: db
spec:
  selector:
    matchLabels: {app: db}
  template:
    metadata:
      labels: {app: db}
    spec:
      containers:
        - name: db
          image: pgvector/pgvector:pg17
          env:
            - {name: POSTGRES_USER, value: assay}
            - {name: POSTGRES_PASSWORD, value: helm-smoke}
            - {name: POSTGRES_DB, value: assay}
          ports:
            - containerPort: 5432
          readinessProbe:
            exec: {command: [pg_isready, -U, assay, -d, assay]}
            periodSeconds: 2
---
apiVersion: v1
kind: Service
metadata:
  name: db
spec:
  selector: {app: db}
  ports:
    - port: 5432
YAML
kubectl --context "$ctx" -n "$ns" rollout status deployment/db --timeout=5m

say "installing the chart"
helm --kube-context "$ctx" upgrade --install assay "$root/deploy/helm/assay" -n "$ns" \
	--set image.repository=assay --set image.tag=helm-smoke --set image.pullPolicy=Never \
	--set database.url='postgres://assay:helm-smoke@db:5432/assay?sslmode=disable' \
	--set worker.replicas=2 \
	--wait --timeout 5m
kubectl --context "$ctx" -n "$ns" rollout status deployment/assay-assay-worker --timeout=5m

port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
kubectl --context "$ctx" -n "$ns" port-forward svc/assay-assay "$port:80" >/dev/null 2>&1 &
forward=$!
i=0
until curl --noproxy '*' -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -ge 60 ]; then
		kubectl --context "$ctx" -n "$ns" logs deployment/assay-assay --tail 50 >&2 || true
		say "/healthz does not answer"
		exit 1
	fi
	sleep 1
done
curl --noproxy '*' -fsS "http://127.0.0.1:$port/readyz" >/dev/null || {
	say "/readyz fails"
	exit 1
}
ready=$(kubectl --context "$ctx" -n "$ns" get deployment assay-assay-worker -o jsonpath='{.status.readyReplicas}')
[ "$ready" = 2 ] || {
	say "$ready of 2 workers are ready"
	exit 1
}
token=$(kubectl --context "$ctx" -n "$ns" get secret assay-assay-keys -o jsonpath='{.data.ASSAY_SETUP_TOKEN}')
[ -n "$token" ] || {
	say "the chart made no setup token"
	exit 1
}

# An upgrade keeps the generated keys.
key=$(kubectl --context "$ctx" -n "$ns" get secret assay-assay-keys -o jsonpath='{.data.ASSAY_SECRET_KEY}')
helm --kube-context "$ctx" upgrade assay "$root/deploy/helm/assay" -n "$ns" --reuse-values --wait --timeout 5m >/dev/null
again=$(kubectl --context "$ctx" -n "$ns" get secret assay-assay-keys -o jsonpath='{.data.ASSAY_SECRET_KEY}')
[ "$key" = "$again" ] || {
	say "the upgrade changed the secret key"
	exit 1
}
say "the chart installs, serves /healthz and /readyz, runs 2 workers and keeps its keys on upgrade"
