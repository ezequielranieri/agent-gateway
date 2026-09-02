#!/usr/bin/env bash
# k8s-patch-secrets.sh
# Patches Kubernetes secrets for agent-gateway in k8s
# Usage: ./k8s-patch-secrets.sh [staging|production] [namespace]

set -euo pipefail

ENVIRONMENT="${1:-staging}"
NAMESPACE="${2:-agent-gateway}"

if [[ "$ENVIRONMENT" == "production" ]]; then
    NEW_POSTGRES_PASSWORD="3Dhjt2CMqRTMoo1o/KE8QA6XzZsPy6iJTtk2Ut1J9H8="
    NEW_REDIS_PASSWORD="dR4spCzdxHASsrJnXhn+iXvra/+gKE3fqVEHsiJicoY="
    NEW_JWT_SECRET="h4JVHasBFB+uf8shhN1dM40pFOieEnYkR/hXGxkm9/k="
else
    NEW_POSTGRES_PASSWORD="Ul2hB2a2/+MVgYyrjVTo4kGAu0c3SjeZRqSkHlNWqeA="
    NEW_REDIS_PASSWORD="0LjAe9028pTSBvUzV1yUDCFVDovDfdulPMTclbKMd20="
    NEW_JWT_SECRET="LjuSfu1EBBGszVg+9OFQeAaGZNEMclhqQpzTO17LRCg="
fi

echo "Patching Kubernetes secrets in namespace: $NAMESPACE"

# Patch the secret (assuming secret name is agent-gateway-secrets)
kubectl -n "$NAMESPACE" patch secret agent-gateway-secrets \
  --type='json' \
  -p="[
    {\"op\": \"replace\", \"path\": \"/data/POSTGRES_PASSWORD\", \"value\": \"$(echo -n \"$NEW_POSTGRES_PASSWORD\" | base64 -w0)\" },
    {\"op\": \"replace\", \"path\": \"/data/REDIS_PASSWORD\", \"value\": \"$(echo -n \"$NEW_REDIS_PASSWORD\" | base64 -w0)\" },
    {\"op\": \"replace\", \"path\": \"/data/JWT_SECRET\", \"value\": \"$(echo -n \"$NEW_JWT_SECRET\" | base64 -w0)\" }
  ]"

# Rollout restart the deployment
kubectl -n "$NAMESPACE" rollout restart deployment/agent-gateway

# Wait for rollout
kubectl -n "$NAMESPACE" rollout status deployment/agent-gateway --timeout=300s

echo "✅ Kubernetes secrets patched and deployment restarted"
echo ""
echo "Verify:"
echo "  kubectl -n $NAMESPACE logs -l app=agent-gateway -f"
echo "  curl https://your-domain/health"