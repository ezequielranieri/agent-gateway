#!/usr/bin/env bash
# Canary deployment script for agent-gateway
# Supports: promote, rollback, status

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Default values
ENVIRONMENT="${ENVIRONMENT:-staging}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.$ENVIRONMENT.yml}"
ENV_FILE="${ENV_FILE:-.env.$ENVIRONMENT}"
CANARY_LABEL="agent-gateway-canary"
MAIN_LABEL="agent-gateway-main"
CANARY_PORT=8081
MAIN_PORT=8080
HEALTH_ENDPOINT="/health"
METRICS_ENDPOINT="/metrics"
PROMOTION_THRESHOLD=95  # Minimum success rate % for promotion
MONITOR_DURATION=600    # Monitor for 10 minutes

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_debug() { echo -e "${BLUE}[DEBUG]${NC} $*"; }

# Check if container is healthy
check_health() {
    local container_name="$1"
    local port="$2"

    for i in {1..30}; do
        if docker exec "$container_name" wget -q --spider "http://localhost:$port$HEALTH_ENDPOINT" 2>/dev/null; then
            return 0
        fi
        sleep 2
    done
    return 1
}

# Get metrics from container
get_metrics() {
    local container_name="$1"
    local port="$2"

    docker exec "$container_name" wget -q -O - "http://localhost:$port$METRICS_ENDPOINT" 2>/dev/null || echo ""
}

# Calculate success rate from metrics
calculate_success_rate() {
    local metrics="$1"
    local success_rate=$(echo "$metrics" | grep -E 'http_requests_total.*status="2[0-9][0-9]"' | awk '{sum+=$2} END {print sum}')
    local total=$(echo "$metrics" | grep 'http_requests_total' | awk '{sum+=$2} END {print sum}')

    if [[ -n "$success_rate" && -n "$total" && "$total" -gt 0 ]]; then
        echo "scale=2; $success_rate * 100 / $total" | bc
    else
        echo "0"
    fi
}

# Deploy canary
deploy_canary() {
    local image_tag="${1:-latest}"

    log_info "Deploying canary with image tag: $image_tag"

    # Pull image
    docker pull "ghcr.io/ezequielranieri/agent-gateway:$image_tag"

    # Stop existing canary if running
    docker stop "$CANARY_LABEL" 2>/dev/null || true
    docker rm "$CANARY_LABEL" 2>/dev/null || true

    # Start canary container with different port
    docker run -d \
        --name "$CANARY_LABEL" \
        --network container:$(docker ps -qf "name=agent-gateway-main") \
        -e AG_SERVER_ADDR=":$CANARY_PORT" \
        -e AG_SERVER_ENV="canary" \
        --label "deployment=canary" \
        "ghcr.io/ezequielranieri/agent-gateway:$image_tag"

    log_info "Waiting for canary to be healthy..."
    if ! check_health "$CANARY_LABEL" "$CANARY_PORT"; then
        log_error "Canary health check failed"
        docker logs "$CANARY_LABEL"
        return 1
    fi

    log_info "Canary deployed successfully on port $CANARY_PORT"
    return 0
}

# Monitor canary
monitor_canary() {
    log_info "Monitoring canary for $MONITOR_DURATION seconds..."
    log_info "Success rate threshold for promotion: $PROMOTION_THRESHOLD%"

    local start_time=$(date +%s)
    local end_time=$((start_time + MONITOR_DURATION))

    while [[ $(date +%s) -lt $end_time ]]; do
        local metrics=$(get_metrics "$CANARY_LABEL" "$CANARY_PORT")
        local success_rate=$(calculate_success_rate "$metrics")

        log_info "Current success rate: ${success_rate}%"

        if (( $(echo "$success_rate < $PROMOTION_THRESHOLD" | bc -l) )); then
            log_error "Success rate ($success_rate%) below threshold ($PROMOTION_THRESHOLD%)"
            return 1
        fi

        sleep 30
    done

    log_info "Monitoring completed successfully"
    return 0
}

# Promote canary to main
promote_canary() {
    log_info "Promoting canary to main..."

    # Stop main container
    docker stop "$MAIN_LABEL" 2>/dev/null || true
    docker rm "$MAIN_LABEL" 2>/dev/null || true

    # Rename canary to main
    docker rename "$CANARY_LABEL" "$MAIN_LABEL"

    # Update docker-compose to use new image
    local image_tag=$(docker inspect "$MAIN_LABEL" --format '{{.Config.Image}}')
    sed -i "s|image: .*|image: $image_tag|" "$COMPOSE_FILE"

    # Restart with docker-compose
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --no-deps gateway

    log_info "Canary promoted to main successfully"
}

# Rollback canary
rollback_canary() {
    log_warn "Rolling back canary deployment..."

    # Stop canary
    docker stop "$CANARY_LABEL" 2>/dev/null || true
    docker rm "$CANARY_LABEL" 2>/dev/null || true

    # Ensure main is running
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d gateway

    log_info "Rollback completed"
}

# Show status
show_status() {
    log_info "=== Canary Deployment Status ==="
    echo "Environment: $ENVIRONMENT"
    echo "Compose file: $COMPOSE_FILE"
    echo ""

    # Main container status
    if docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | grep -q "$MAIN_LABEL"; then
        echo -e "${GREEN}Main:${NC} Running"
        docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | grep "$MAIN_LABEL"
    else
        echo -e "${RED}Main:${NC} Not running"
    fi

    echo ""

    # Canary container status
    if docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | grep -q "$CANARY_LABEL"; then
        echo -e "${YELLOW}Canary:${NC} Running"
        docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | grep "$CANARY_LABEL"
    else
        echo -e "${BLUE}Canary:${NC} Not deployed"
    fi
}

# Usage
usage() {
    cat <<EOF
Usage: $0 [command] [options]

Commands:
  deploy [image_tag]    Deploy canary (default image: latest)
  monitor               Monitor canary metrics
  promote               Promote canary to main
  rollback              Rollback canary deployment
  status                Show deployment status
  full [image_tag]      Full canary flow: deploy -> monitor -> promote

Environment Variables:
  ENVIRONMENT          Target environment (staging|prod) [default: staging]
  COMPOSE_FILE         Docker compose file [default: docker-compose.staging.yml]
  ENV_FILE             Environment file [default: .env.staging]
  PROMOTION_THRESHOLD  Min success rate for promotion [default: 95]
  MONITOR_DURATION     Monitor duration in seconds [default: 600]

Examples:
  $0 deploy v1.0.0-rc2
  $0 full latest
  $0 promote
  $0 rollback
  $0 status

EOF
}

# Main
main() {
    local command="${1:-help}"

    case "$command" in
        deploy)
            deploy_canary "${2:-latest}"
            ;;
        monitor)
            monitor_canary
            ;;
        promote)
            promote_canary
            ;;
        rollback)
            rollback_canary
            ;;
        status)
            show_status
            ;;
        full)
            deploy_canary "${2:-latest}" && monitor_canary && promote_canary
            ;;
        help|--help|-h)
            usage
            ;;
        *)
            log_error "Unknown command: $command"
            usage
            exit 1
            ;;
    esac
}

main "$@"