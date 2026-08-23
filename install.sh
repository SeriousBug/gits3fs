#!/bin/sh
# Installs git-s3fs by downloading the latest release binary for your
# platform. Usage:
#
#   curl -fsSL https://raw.githubusercontent.com/SeriousBug/gits3fs/main/install.sh | sh
#
# Set VERSION to install a specific release instead of the latest, e.g.:
#
#   curl -fsSL .../install.sh | VERSION=v0.1.0 sh

set -e

REPO="SeriousBug/gits3fs"

# Directories checked, in order, for an existing spot on PATH to install
# into. If none of these are on PATH, we fall back to the first one.
CANDIDATE_DIRS="$HOME/.local/bin $HOME/bin $HOME/.bin"

info() { printf '%s\n' "$*"; }
error() { printf 'error: %s\n' "$*" >&2; exit 1; }

detect_platform() {
	os=$(uname -s)
	arch=$(uname -m)

	case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	FreeBSD) os=freebsd ;;
	MINGW* | MSYS* | CYGWIN*) os=windows ;;
	*) error "unsupported OS: $os" ;;
	esac

	case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) error "unsupported architecture: $arch" ;;
	esac

	ext=""
	if [ "$os" = "windows" ]; then
		ext=".exe"
	fi
}

resolve_version() {
	if [ -n "${VERSION:-}" ]; then
		return
	fi
	VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		grep '"tag_name"' | head -n1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
	[ -n "$VERSION" ] || error "could not determine the latest release version"
}

find_install_dir() {
	# Prefer a candidate directory that is already on PATH.
	for dir in $CANDIDATE_DIRS; do
		case ":$PATH:" in
		*":$dir:"*)
			INSTALL_DIR="$dir"
			ON_PATH=1
			return
			;;
		esac
	done

	# None on PATH: fall back to the first candidate and warn later.
	INSTALL_DIR=$(echo "$CANDIDATE_DIRS" | cut -d' ' -f1)
	ON_PATH=0
}

path_hint() {
	shell_name=$(basename "${SHELL:-sh}")
	case "$shell_name" in
	fish)
		rc="~/.config/fish/config.fish"
		line="fish_add_path $INSTALL_DIR"
		;;
	zsh)
		rc="~/.zshrc"
		line="echo 'export PATH=\"\$PATH:$INSTALL_DIR\"' >> $rc"
		;;
	*)
		rc="~/.bashrc (or ~/.bash_profile, ~/.profile)"
		line="echo 'export PATH=\"\$PATH:$INSTALL_DIR\"' >> ~/.bashrc"
		;;
	esac

	info ""
	info "$INSTALL_DIR is not on your PATH yet. Add it with:"
	info ""
	info "    $line"
	info ""
	info "then restart your shell (or 'source $rc')."

	if [ "$os" = windows ]; then
		info ""
		info "On native Windows (not Git Bash), add it instead via:"
		info ""
		info "    setx PATH \"%PATH%;$(cygpath -w "$INSTALL_DIR" 2>/dev/null || echo "$INSTALL_DIR")\""
		info ""
		info "then open a new terminal."
	fi
}

main() {
	detect_platform
	resolve_version
	find_install_dir

	asset="git-s3fs-$os-$arch$ext"
	url="https://github.com/$REPO/releases/download/$VERSION/$asset"

	mkdir -p "$INSTALL_DIR"
	tmp=$(mktemp)
	trap 'rm -f "$tmp"' EXIT

	info "Downloading $asset ($VERSION)..."
	curl -fsSL "$url" -o "$tmp" || error "failed to download $url"

	dest="$INSTALL_DIR/git-s3fs$ext"
	mv "$tmp" "$dest"
	chmod +x "$dest"
	trap - EXIT

	info "Installed git-s3fs to $dest"

	if [ "$ON_PATH" = "1" ]; then
		info "$INSTALL_DIR is already on your PATH. Run 'git s3fs --help' to get started."
	else
		path_hint
	fi
}

main
