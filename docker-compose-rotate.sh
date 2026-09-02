#!/usr/bin/env bash
# docker-compose-rotate.sh
# Rotates credentials for docker-compose deployments
# Usage: ./docker-compose-rotate.sh [staging|production]

set -euo pipefail

ENVIRONMENT="${1:-staging}"
COMPOSE_FILE="docker-compose.${ENVIRONMENT}.yml"

if [[ "$ENVIRONMENT" == "production" ]]; then
    NEW_POSTGRES_PASSWORD="3Dhjt2CMqRTMoo1o/KE8QA6XzZsPy6iJTtk2Ut1J9H8="
    NEW_REDIS_PASSWORD="dR4spCzdxHASsrJnXhn+iXvra/+gKE3fqVEHsiJicoY="
    NEW_JWT_SECRET="h4JVHasBFB+uf8shhN1dM40pFOieEnYkR/hXGxkm9/k="
    NEW_POSTGRES_PASSWORD_ENCODED="3Dhjt2CMqRTMoo1o%2FKE8QA6XzZsPy6iJTtk2Ut1J9H8%3D"
else
    NEW_POSTGRES_PASSWORD="Ul2hB2a2/+MVgYyrjVTo4kGAu0c3SjeZRqSkHlNWqeA="
    NEW_REDIS_PASSWORD="0LjAe9028pTSBvUzV1yUDCFVDovDfdulPMTclbKMd20="
    NEW_JWT_SECRET="LjuSfu1EBBGszVg+9OFQeAaGZNEMclhqQpzTO17LRCg="
    NEW_POSTGRES_PASSWORD_ENCODED="Ul2hB2a2%2FMVgYyrjVTo4kGAu0c3SjeZRqSkHlNWqeA%3D"
fi

ENV_FILE=".env.${ENVIRONMENT}"

echo "=========================================="
echo "Rotating docker-compose credentials for: $ENVIRONMENT"
echo "=========================================="

# Check if .env file exists
if [[ ! -f "$ENV_FILE" ]]; then
    echo "❌ $ENV_FILE not found"
    exit 1
fi

# Backup original
cp "$ENV_FILE" "${ENV_FILE}.bak.$(date +%s)"
echo "✅ Backed up $ENV_FILE"

# Update .env file
sed -i \
    -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$NEW_POSTGRES_PASSWORD|" \
    -e "s|^POSTGRES_PASSWORD_ENCODED=.*|POSTGRES_PASSWORD_ENCODED=$NEW_POSTGRES_PASSWORD_ENCODED|" \
    -e "s|^REDIS_PASSWORD=.*|REDIS_PASSWORD=$NEW_REDIS_PASSWORD|" \
    -e "s|^JWT_SECRET=.*|JWT_SECRET=$NEW_JWT_SECRET|" \
    "$ENV_FILE"

echo "✅ Updated $ENV_FILE with new credentials"

# Recreate containers with new env
echo "Recreating containers..."
docker-compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --force-recreate

# Wait for health checks
echo "Waiting for services to be healthy..."
sleep 10

# Check health
for i in {1..30}; do
    if curl -sf http://localhost:8080/health >/dev/null 2>&1; then
        echo "✅ Gateway is healthy"
        break
    fi
    sleep 2
done

echo ""
echo "✅ Rotation complete for $ENVIRONMENT"
echo ""
echo "New credentials:"
echo "  POSTGRES_PASSWORD=$NEW_POSTGRES_PASSWORD"
echo "  REDIS_PASSWORD=$NEW_REDIS_PASSWORD"
echo "  JWT_SECRET=$NEW_JWT_SECRET"
echo ""
echo "Verify:"
echo "  curl http://localhost:8080/health"
echo "  Test login + chat/completions + HITL flow"