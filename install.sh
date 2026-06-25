#!/bin/bash
set -e

# Configuration
REPO="${REPO:-OpenListTeam/OpenList}"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/openlist"
DATA_DIR="/var/lib/openlist"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Options:
    -r, --repo <user/repo>   GitHub repository (default: OpenListTeam/OpenList)
    -t, --tag <tag>          Release tag (default: latest)
    -p, --prefix <path>      Install prefix (default: /usr/local)
    -h, --help              Show this help message

Examples:
    # Install latest version from official repo
    sudo bash install.sh

    # Install specific version from your fork
    sudo bash install.sh -r yourusername/OpenList -t v4.0.0
EOF
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -r|--repo)
            REPO="$2"
            shift 2
            ;;
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        -p|--prefix)
            PREFIX="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Detect system
detect_system() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case $ARCH in
        x86_64)
            ARCH="amd64"
            ;;
        aarch64|arm64)
            ARCH="arm64"
            ;;
        armv7l)
            ARCH="arm"
            ;;
        *)
            log_error "Unsupported architecture: $ARCH"
            exit 1
            ;;
    esac

    # Check for musl (Alpine, etc.)
    if [ -f /etc/alpine-release ] || (command -v apk &> /dev/null && apk info --mode 2>/dev/null | grep -q musl); then
        EXT="musl"
    else
        EXT=""
    fi

    case $OS in
        linux)
            case $EXT in
                musl)
                    FILENAME="openlist-linux-${ARCH}-musl.tar.gz"
                    ;;
                *)
                    FILENAME="openlist-linux-${ARCH}.tar.gz"
                    ;;
            esac
            ;;
        darwin)
            FILENAME="openlist-darwin-${ARCH}.tar.gz"
            ;;
        mingw*|cygwin*|msys*)
            OS="windows"
            FILENAME="openlist-windows-${ARCH}.zip"
            ;;
        *)
            log_error "Unsupported OS: $OS"
            exit 1
            ;;
    esac

    log_info "Detected: ${OS} ${ARCH}${EXT:+ (musl)}"
}

# Get latest version
get_version() {
    if [ -z "$TAG" ]; then
        log_info "Fetching latest version..."
        TAG=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep -o '"tag_name":.*' | sed 's/.*"tag_name":.*"//;s/"//' | sed 's/^v//')
        if [ -z "$TAG" ]; then
            log_error "Failed to fetch latest version"
            exit 1
        fi
    fi
    log_info "Version: ${TAG}"
}

# Download
download() {
    local filename="$1"
    local url="https://github.com/${REPO}/releases/download/v${TAG}/${filename}"

    log_info "Downloading ${filename}..."
    log_info "From: ${url}"

    if command -v curl &> /dev/null; then
        curl -fsSL -o "/tmp/${filename}" "${url}" || { log_error "Download failed"; exit 1; }
    elif command -v wget &> /dev/null; then
        wget -q -O "/tmp/${filename}" "${url}" || { log_error "Download failed"; exit 1; }
    else
        log_error "Neither curl nor wget found"
        exit 1
    fi
}

# Install
install() {
    local filename="$1"
    local temp_dir="/tmp/openlist_install_$$"

    mkdir -p "${temp_dir}"
    cd "${temp_dir}"

    log_info "Extracting..."
    case $filename in
        *.tar.gz)
            tar xzf "/tmp/${filename}" || { log_error "Extract failed"; exit 1; }
            ;;
        *.zip)
            unzip -q "/tmp/${filename}" || { log_error "Extract failed"; exit 1; }
            ;;
    esac

    # Find the binary
    local binary=$(find . -name "openlist*" -type f -executable 2>/dev/null | head -1)
    if [ -z "$binary" ]; then
        log_error "Binary not found in archive"
        exit 1
    fi

    log_info "Installing to ${INSTALL_DIR}/openlist..."

    # Create directories
    mkdir -p "${INSTALL_DIR}" || { log_error "Failed to create ${INSTALL_DIR}"; exit 1; }
    mkdir -p "${CONFIG_DIR}" || { log_error "Failed to create ${CONFIG_DIR}"; exit 1; }
    mkdir -p "${DATA_DIR}" || { log_error "Failed to create ${DATA_DIR}"; exit 1; }

    # Install binary
    cp "${binary}" "${INSTALL_DIR}/openlist"
    chmod +x "${INSTALL_DIR}/openlist"

    # Cleanup
    cd /
    rm -rf "${temp_dir}" "/tmp/${filename}"

    log_info "Installation complete!"
    log_info ""
    log_info "Run 'openlist server' to start"
    log_info "Default config at: ${CONFIG_DIR}/config.json"
    log_info "Default data at: ${DATA_DIR}"
}

# Main
main() {
    log_info "OpenList Installer"
    log_info "Repository: ${REPO}"
    log_info ""

    detect_system
    get_version
    download "${FILENAME}"
    install "${FILENAME}"
}

main
