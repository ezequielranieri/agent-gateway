#!/usr/bin/env bash
# rotate-credentials.sh
# Rotates PostgreSQL, Redis, and JWT credentials for agent-gateway
# Usage: sudo ./rotate-credentials.sh [staging|production]

set -euo pipefail

ENVIRONMENT="${1:-staging}"

# New credentials (rotated 2026-09-01)
if [[ "$ENVIRONMENT" == "production" ]]; then
    NEW_POSTGRES_PASSWORD="3Dhjt2CMqRTMoo1o/KE8QA6XzZsPy6iJTtk2Ut1J9H8="
    NEW_REDIS_PASSWORD="dR4spCzdxHASsrJnXhn+iXvra/+gKE3fqVEHsiJicoY="
    NEW_JWT_SECRET="h4JVHasBFB+uf8shhN1dM40pFOieEnYkR/hXGxkm9/k="
    ENV_NAME="production"
else
    NEW_POSTGRES_PASSWORD="Ul2hB2a2/+MVgYyrjVTo4kGAu0c3SjeZRqSkHlNWqeA="
    NEW_REDIS_PASSWORD="0LjAe9028pTSBvUzV1yUDCFVDovDfdulPMTclbKMd20="
    NEW_JWT_SECRET="LjuSfu1EBBGszVg+9OFQeAaGZNEMclhqQpzTO17LRCg="
    ENV_NAME="staging"
fi

POSTGRES_USER="gateway"
POSTGRES_DB="agent_gateway"
REDIS_CLI="${REDIS_CLI:-redis-cli}"

echo "=========================================="
echo "Rotating credentials for: $ENV_NAME"
echo "=========================================="

# ---- PostgreSQL ----
echo "[1/3] Rotating PostgreSQL password..."
if command -v psql >/dev/null 2>&1; then
    # Test current connection first
    if PGPASSWORD="$OLD_POSTGRES_PASSWORD" psql -h localhost -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT 1;" >/dev/null 2>&1; then
        echo "  Current connection OK"
    else
        echo "  WARNING: Could not connect with old password (maybe already rotated?)"
    fi

    # Rotate password
    echo "  Setting new password for user: $POSTGRES_USER"
    psql -h localhost -U postgres -d postgres -c "ALTER USER $POSTGRES_USER WITH PASSWORD '$NEW_POSTGRES_PASSWORD';"
    
    # Verify new connection
    if PGPASSWORD="$NEW_POSTGRES_PASSWORD" psql -h localhost -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT 1;" >/dev/null 2>&1; then
        echo "  ✅ PostgreSQL password rotated successfully"
    else
        echo "  ❌ FAILED: Could not connect with new password"
        exit 1
    fi
else
    echo "  ⚠️  psql not found, skipping PostgreSQL rotation"
fi

# ---- Redis ----
echo "[2/3] Rotating Redis password..."
if command -v "$REDIS_CLI" >/dev/null 2>&1; then
    # Try to connect with old password (may fail if already rotated)
    OLD_REDIS_PASS="${OLD_REDIS_PASSWORD:-}"
    if [[ -n "$OLD_REDIS_PASS" ]] && "$REDIS_CLI" -a "$OLD_REDIS_PASS" ping >/dev/null 2>&1; then
        echo "  Current connection OK, rotating..."
        "$REDIS_CLI" -a "$OLD_REDIS_PASS" CONFIG SET requirepass "$NEW_REDIS_PASSWORD"
    else
        # Try with new password (maybe already rotated)
        if "$REDIS_CLI" -a "$NEW_REDIS_PASSWORD" ping >/dev/null 2>&1; then
            echo "  Already using new password"
        else
            echo "  ⚠️  Could not connect with old or new password. Trying without auth..."
            if "$REDIS_CLI" ping >/dev/null 2>&1; then
                echo "  Redis has no password set. Setting new password..."
                "$REDIS_CLI" CONFIG SET requirepass "$NEW_REDIS_PASSWORD"
            else
                echo "  ❌ Cannot connect to Redis"
                exit 1
            fi
        fi
    fi

    # Verify new password works
    if "$REDIS_CLI" -a "$NEW_REDIS_PASSWORD" ping >/dev/null 2>&1; then
        echo "  ✅ Redis password rotated successfully"
        # Persist to disk
        "$REDIS_CLI" -a "$NEW_REDIS_PASSWORD" CONFIG REWRITE >/dev/null 2>&1 || true
    else
        echo "  ❌ FAILED: Redis not accepting new password"
        exit 1
    fi
else
    echo "  ⚠️  redis-cli not found, skipping Redis rotation"
fi

# ---- JWT Secret ----
echo "[3/3] JWT Secret rotation..."
echo "  New JWT_SECRET for $ENV_NAME:"
echo "  $NEW_JWT_SECRET"
echo ""
echo "  ⚠️  MANUAL STEP REQUIRED:"
echo "  1. Update your deployment (systemd/env file/k8s secret/.env file) with:"
echo "     AG_JWT_SECRET=$NEW_JWT_SECRET"
echo "     JWT_SECRET=$NEW_JWT_SECRET"
echo "  2. Restart the gateway service (rolling restart recommended):"
echo "     systemctl restart agent-gateway    # or: kubectl rollout restart deployment/agent-gateway"
echo "  3. Verify: curl http://localhost:8080/health && test login flow"
echo ""

# Summary
echo "=========================================="
echo "✅ Rotation complete for $ENV_NAME"
echo "=========================================="
echo ""
echo "New credentials (SAVE THESE SECURELY):"
echo "  POSTGRES_PASSWORD=$NEW_POSTGRES_PASSWORD"
echo "  REDIS_PASSWORD=$NEW_REDIS_PASSWORD"
echo "  JWT_SECRET=$NEW_JWT_SECRET"
echo ""
echo "Next steps:"
echo "1. Update your deployment/config with new JWT_SECRET"
echo "2. Rolling restart gateway pods/services"
echo "4. Test: login → chat/completions → HITL flow"
echo "5. Update your password manager / secret store"