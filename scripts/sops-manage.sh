#!/usr/bin/env bash
# SOPS secret management script for agent-gateway
# Usage: ./scripts/sops-manage.sh [encrypt|decrypt|edit|rotate] <environment>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# Check if sops and age are installed
check_tools() {
    for tool in sops age age-keygen; do
        if ! command -v "$tool" &> /dev/null; then
            log_error "$tool is not installed. Please install it first."
            exit 1
        fi
    done
}

# Generate age keys for team members
generate_keys() {
    log_info "Generating age key pair..."
    age-keygen -o "$PROJECT_ROOT/keys/age-key.txt"
    log_info "Keys generated at $PROJECT_ROOT/keys/age-key.txt"
    log_warn "IMPORTANT: Store the private key securely! The public key goes in .sops.yaml"
}

# Encrypt a file
encrypt_file() {
    local env_file="$1"
    local env="${2:-staging}"

    if [[ ! -f "$env_file" ]]; then
        log_error "File $env_file not found"
        exit 1
    fi

    local encrypted_file="${env_file}.enc"
    log_info "Encrypting $env_file -> $encrypted_file"

    sops --encrypt \
        --age "$(grep -A 2 "path_regex: \\\\.env\\\\.$env" "$PROJECT_ROOT/.sops.yaml" | grep age | head -1 | awk '{print $2}')" \
        "$env_file" > "$encrypted_file"

    log_info "Encrypted file created: $encrypted_file"
}

# Decrypt a file
decrypt_file() {
    local encrypted_file="$1"
    local output_file="${2:-}"

    if [[ ! -f "$encrypted_file" ]]; then
        log_error "File $encrypted_file not found"
        exit 1
    fi

    if [[ -z "$output_file" ]]; then
        output_file="${encrypted_file%.enc}"
    fi

    log_info "Decrypting $encrypted_file -> $output_file"
    sops --decrypt "$encrypted_file" > "$output_file"
    log_info "Decrypted file created: $output_file"
}

# Edit encrypted file
edit_file() {
    local encrypted_file="$1"

    if [[ ! -f "$encrypted_file" ]]; then
        log_error "File $encrypted_file not found"
        exit 1
    fi

    log_info "Opening $encrypted_file for editing..."
    sops "$encrypted_file"
}

# Rotate keys
rotate_keys() {
    log_info "Rotating encryption keys..."
    sops --rotate "$PROJECT_ROOT/.env.staging.enc" 2>/dev/null || true
    sops --rotate "$PROJECT_ROOT/.env.prod.enc" 2>/dev/null || true
    log_info "Keys rotated"
}

# Show usage
usage() {
    cat <<EOF
Usage: $0 [command] [environment]

Commands:
  generate-keys    Generate new age key pair
  encrypt <file> [env]   Encrypt a .env file (env: staging|prod)
  decrypt <file> [output] Decrypt an encrypted file
  edit <file>      Edit an encrypted file in \$EDITOR
  rotate           Rotate encryption keys for all encrypted files
  help             Show this help

Examples:
  $0 generate-keys
  $0 encrypt .env.staging staging
  $0 decrypt .env.staging.enc .env.staging
  $0 edit .env.staging.enc
  $0 rotate

EOF
}

# Main
main() {
    check_tools

    local command="${1:-help}"
    case "$command" in
        generate-keys)
            generate_keys
            ;;
        encrypt)
            encrypt_file "${2:-}" "${3:-staging}"
            ;;
        decrypt)
            decrypt_file "${2:-}" "${3:-}"
            ;;
        edit)
            edit_file "${2:-}"
            ;;
        rotate)
            rotate_keys
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