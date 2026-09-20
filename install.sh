#!/bin/sh
#
# Install lms-sync — https://github.com/sak0x7d5/lms-sync
#
#   curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh
#
# Works out what this machine is, downloads the one binary that matches from a
# GitHub release, checks it against that release's SHA256SUMS, and puts
# `lms-sync` on your PATH.
#
# The binary gets a directory of its own, and that is not tidiness: lms-sync
# keeps config.toml, manifest.json and its Google token *beside its own
# executable*. What goes on PATH is a symlink, which the tool resolves back to
# the real file — so its settings follow the binary instead of being written
# into your bin directory.
#
# Nothing here asks a question. Under `curl | sh` there is no terminal to read
# an answer from, so every choice is a flag or an environment variable; run
# with --help to see them.

set -eu

# ---------------------------------------------------------------------------
# What this installs
# ---------------------------------------------------------------------------

REPO='sak0x7d5/lms-sync'
BIN_NAME='lms-sync'

# The targets release.yml builds. Keep this list and that matrix the same, or
# this script offers a machine a file that was never compiled for it.
SUPPORTED='linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64'

# Releases before this one were published without a SHA256SUMS asset, so
# pinning to one of them cannot be verified. Named in the error rather than
# guessed at, so nobody has to read the workflow to find out.
FIRST_CHECKSUMMED='v1.3.0'

# Delimits the block written into a shell profile, so it can be found again
# and taken back out by --uninstall.
MARK_BEGIN='# >>> lms-sync installer >>>'
MARK_END='# <<< lms-sync installer <<<'

# Overridable so the tests can serve a release from a local origin. Nothing
# else has a reason to set it.
BASE_URL="${LMS_SYNC_BASE_URL:-https://github.com/$REPO/releases}"

WANT_VERSION="${LMS_SYNC_VERSION:-}"
INSTALL_DIR_OPT="${LMS_SYNC_INSTALL_DIR:-}"
BIN_DIR_OPT="${LMS_SYNC_BIN_DIR:-}"
SKIP_CHECKSUM=no
ACTION=install
TMP_DIR=''
TAG=''
ADOPTED=no

if [ -n "${LMS_SYNC_NO_MODIFY_PATH:-}" ]; then
	MODIFY_PATH=no
else
	MODIFY_PATH=yes
fi

# ---------------------------------------------------------------------------
# Saying things
# ---------------------------------------------------------------------------

say() { printf '%s\n' "$*"; }
step() { printf '  %-12s %s\n' "$1" "$2"; }
warn() { printf '\nWarning: %s\n' "$*" >&2; }

# die reports the way the tool itself does (see reportErr in main.go): the
# kind in brackets, then the hint indented under a blank line. The kinds are
# lms-sync's own, from errors.go — a seventh invented here would be drift.
# `config` exits 2 for the same reason it does there: the run was asked for
# something it cannot do, rather than having failed at it.
die() {
	printf '\nError [%s]: %s\n' "$1" "$2" >&2
	if [ -n "${3:-}" ]; then
		printf '\n' >&2
		printf '%s\n' "$3" | while IFS= read -r die_line; do
			printf '  %s\n' "$die_line" >&2
		done
	fi
	case "$1" in
	config) exit 2 ;;
	*) exit 1 ;;
	esac
}

usage() {
	# Named literally rather than with $0: under `curl | sh` that is "sh".
	cat <<'USAGE'
install.sh — install lms-sync and put it on your PATH

  curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh

Options (pass them after `| sh -s --`):

  --version TAG        install a particular release, e.g. --version v1.3.0
  --install-dir DIR    where the binary and its settings live
  --bin-dir DIR        where the symlink that puts it on PATH goes
  --no-modify-path     do not touch any shell profile
  --skip-checksum      install without verifying SHA256SUMS
  --uninstall          remove the binary, the symlink and the PATH line
  -h, --help           this

Each has an environment variable: LMS_SYNC_VERSION, LMS_SYNC_INSTALL_DIR,
LMS_SYNC_BIN_DIR, LMS_SYNC_NO_MODIFY_PATH. A flag wins over the variable.

  curl -fsSL .../install.sh | sh -s -- --version v1.3.0
USAGE
}

# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }

# cleanup runs on every exit, including a die. rmdir rather than `rm -rf` for
# the two directories: it removes them only if this run created them and left
# them empty, so a `--install-dir <typo>` that fails does not leave the typo
# sitting there — the same reason --dry-run refuses to create its destination.
cleanup() {
	if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
		rm -rf "$TMP_DIR"
	fi
	if [ -n "${BIN_DIR:-}" ]; then
		rmdir "$BIN_DIR" 2>/dev/null || true
	fi
	if [ -n "${INSTALL_DIR:-}" ]; then
		rmdir "$INSTALL_DIR" 2>/dev/null || true
	fi
}

normalise_version() {
	case "$1" in
	v*) printf '%s\n' "$1" ;;
	*) printf 'v%s\n' "$1" ;;
	esac
}

# resolve_symlink walks a chain of links by hand: `readlink -f` is GNU-only
# and realpath is not everywhere, and this has to work on a Mac.
resolve_symlink() {
	resolve_path="$1"
	resolve_hops=0
	while [ -L "$resolve_path" ] && [ "$resolve_hops" -lt 8 ]; do
		resolve_target=$(readlink "$resolve_path")
		case "$resolve_target" in
		/*) resolve_path="$resolve_target" ;;
		*) resolve_path="$(dirname "$resolve_path")/$resolve_target" ;;
		esac
		resolve_hops=$((resolve_hops + 1))
	done
	printf '%s\n' "$resolve_path"
}

http_fetch() {
	if [ "$HTTP_CLIENT" = curl ]; then
		case "$1" in
		https://*) curl -fsSL --proto '=https' --retry 3 --retry-delay 1 -o "$2" "$1" ;;
		*) curl -fsSL --retry 3 --retry-delay 1 -o "$2" "$1" ;;
		esac
	else
		wget -q -O "$2" "$1"
	fi
}

# http_get takes "quiet" as a third argument for a fetch whose failure is an
# expected outcome — asking an older release for a checksum file it never had.
# Letting curl print its own "404" there puts a line above an error block that
# already says the same thing, and says it better.
http_get() {
	if [ "${3:-}" = quiet ]; then
		http_fetch "$1" "$2" 2>/dev/null
	else
		http_fetch "$1" "$2"
	fi
}

# http_final_url prints where a redirect chain ends, and nothing if it cannot
# tell. curl only — wget has no way to follow redirects without also
# downloading the page.
http_final_url() {
	if [ "$HTTP_CLIENT" != curl ]; then
		return 0
	fi
	curl -fsSLI -o /dev/null -w '%{url_effective}' --retry 2 "$1" 2>/dev/null || true
}

sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif have shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif have openssl; then
		# OpenSSL 1 prints "SHA256(file)= hash" and OpenSSL 3 prints
		# "SHA2-256(file)= hash", so take the last field rather than the second.
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		return 1
	fi
}

# ---------------------------------------------------------------------------
# What machine is this
# ---------------------------------------------------------------------------

unsupported_hint() {
	printf '%s\n' "lms-sync is built for: $SUPPORTED
Anything else Go targets builds from source in one command:
https://github.com/$REPO#build-from-source"
}

detect_platform() {
	OS=$(uname -s 2>/dev/null || echo unknown)
	case "$OS" in
	Linux) OS=linux ;;
	Darwin) OS=darwin ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT)
		die config "This is Windows, which has an installer of its own." \
			"Run this in PowerShell instead:

  irm https://github.com/$REPO/releases/latest/download/install.ps1 | iex"
		;;
	*)
		die config "lms-sync has no build for $OS." "$(unsupported_hint)"
		;;
	esac

	ARCH=$(uname -m 2>/dev/null || echo unknown)
	case "$ARCH" in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*)
		die config "lms-sync has no build for $OS/$ARCH." "$(unsupported_hint)"
		;;
	esac

	# An Apple-silicon Mac running this script under Rosetta reports x86_64,
	# and the Intel build it would pick then runs translated on a machine that
	# has a native build waiting. Only the kernel knows the difference.
	if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ]; then
		if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
			ARCH=arm64
		fi
	fi

	ASSET="$BIN_NAME-$OS-$ARCH"
}

# is_termux reads the two signals platform.go's isTermux() reads, and for the
# same reason it reads two: nothing about the binary says it is on a phone.
is_termux() {
	if [ -n "${TERMUX_VERSION:-}" ]; then
		return 0
	fi
	case "${PREFIX:-}" in
	*com.termux*) return 0 ;;
	esac
	return 1
}

# ---------------------------------------------------------------------------
# Where it goes
# ---------------------------------------------------------------------------

# find_existing_install looks for a folder lms-sync has already been run from.
# An install is a folder holding config.toml or manifest.json, because that is
# where the tool puts them — beside its own binary.
#
# Installing somewhere else instead of into it is not merely untidy: the new
# location has no manifest, and main.go says what follows — "a manifest the
# sync cannot find means every file in the library looks new and is fetched
# again". That is a whole library re-downloaded, from a tool whose retry
# policy exists to avoid hammering a university's server.
find_existing_install() {
	FOUND_DIR=''
	found_onpath=$(command -v "$BIN_NAME" 2>/dev/null) || found_onpath=''
	if [ -n "$found_onpath" ]; then
		found_onpath=$(dirname "$(resolve_symlink "$found_onpath")")
	fi
	for found_cand in "$found_onpath" "$HOME/lms-sync" "$INSTALL_DIR"; do
		if [ -z "$found_cand" ]; then
			continue
		fi
		if [ -f "$found_cand/config.toml" ] || [ -f "$found_cand/manifest.json" ]; then
			FOUND_DIR="$found_cand"
			return 0
		fi
	done
	return 0
}

resolve_dirs() {
	if [ -z "${HOME:-}" ]; then
		die config "HOME is not set, so there is nowhere obvious to install to." \
			"Pass --install-dir and --bin-dir explicitly."
	fi

	if [ -n "$INSTALL_DIR_OPT" ]; then
		INSTALL_DIR="$INSTALL_DIR_OPT"
	else
		INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/$BIN_NAME"
		find_existing_install
		if [ -n "$FOUND_DIR" ] && [ "$FOUND_DIR" != "$INSTALL_DIR" ]; then
			INSTALL_DIR="$FOUND_DIR"
			ADOPTED=yes
		fi
	fi

	if [ -n "$BIN_DIR_OPT" ]; then
		BIN_DIR="$BIN_DIR_OPT"
	elif is_termux; then
		# Termux's own bin directory is already on PATH, so nothing has to be
		# written to a profile there.
		BIN_DIR="${PREFIX:-/data/data/com.termux/files/usr}/bin"
	else
		BIN_DIR="${XDG_BIN_HOME:-$HOME/.local/bin}"
	fi

	LINK="$BIN_DIR/$BIN_NAME"
	TARGET="$INSTALL_DIR/$BIN_NAME"
}

# ---------------------------------------------------------------------------
# PATH
# ---------------------------------------------------------------------------

# path_has tests for a whole entry, not a substring: without the surrounding
# colons, "/home/u/.local/bin" matches inside "/home/u/.local/binary".
path_has() {
	path_dir="${1%/}"
	case ":${PATH:-}:" in
	*":$path_dir:"*) return 0 ;;
	esac
	return 1
}

# set_profile_files names the files a login shell of this kind actually reads.
# Writing the line into the wrong one is worse than not writing it: it looks
# done and never runs.
#
# Two globals rather than a printed list, because a printed list has to be
# word-split to iterate and a Mac account called "John Smith" has a space in
# every one of these paths.
set_profile_files() {
	PROFILE_A=''
	PROFILE_B=''
	profile_shell=$(basename "${SHELL:-sh}")
	case "$profile_shell" in
	fish)
		PROFILE_A="${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish"
		;;
	zsh)
		PROFILE_A="${ZDOTDIR:-$HOME}/.zshrc"
		;;
	bash)
		PROFILE_A="$HOME/.bashrc"
		# A macOS bash login shell reads .bash_profile and never looks at
		# .profile once that file exists.
		if [ -f "$HOME/.bash_profile" ]; then
			PROFILE_B="$HOME/.bash_profile"
		else
			PROFILE_B="$HOME/.profile"
		fi
		;;
	*)
		PROFILE_A="$HOME/.profile"
		;;
	esac
}

# apply_to_profiles runs $1 over each profile file this shell reads.
apply_to_profiles() {
	set_profile_files
	"$1" "$PROFILE_A"
	if [ -n "$PROFILE_B" ]; then
		"$1" "$PROFILE_B"
	fi
}

add_path_block() {
	if [ -f "$1" ] && grep -qF "$MARK_BEGIN" "$1" 2>/dev/null; then
		return 0
	fi
	mkdir -p "$(dirname "$1")"
	# The block re-checks PATH at source time as well, so a profile sourced
	# twice does not end up with the directory in there twice.
	#
	# The $PATH in these formats is meant to stay literal: it is written into
	# a profile for the student's shell to expand later, not by this one.
	# shellcheck disable=SC2016
	case "$1" in
	*/config.fish)
		{
			printf '\n%s\n' "$MARK_BEGIN"
			printf 'if not contains %s $PATH\n' "$BIN_DIR"
			printf '    set -gx PATH %s $PATH\n' "$BIN_DIR"
			printf 'end\n'
			printf '%s\n' "$MARK_END"
		} >>"$1"
		;;
	*)
		{
			printf '\n%s\n' "$MARK_BEGIN"
			printf 'case ":$PATH:" in\n'
			printf '  *":%s:"*) ;;\n' "$BIN_DIR"
			printf '  *) PATH="%s:$PATH" ;;\n' "$BIN_DIR"
			printf 'esac\n'
			printf 'export PATH\n'
			printf '%s\n' "$MARK_END"
		} >>"$1"
		;;
	esac
	step path "added to $1"
}

remove_path_block() {
	if [ ! -f "$1" ]; then
		return 0
	fi
	if ! grep -qF "$MARK_BEGIN" "$1" 2>/dev/null; then
		return 0
	fi
	cp -p "$1" "$1.lms-sync-backup"
	# `sed -i` is not portable — BSD wants an argument where GNU refuses one.
	# cp -p first so the rewritten file keeps the mode it had; the redirect
	# then truncates that copy and fills it.
	remove_tmp="$1.lms-sync-tmp.$$"
	cp -p "$1" "$remove_tmp"
	awk -v b="$MARK_BEGIN" -v e="$MARK_END" '
		$0 == b { skip = 1; next }
		$0 == e { skip = 0; next }
		!skip   { print }
	' "$1" >"$remove_tmp"
	mv -f "$remove_tmp" "$1"
	step path "removed from $1"
}

# ---------------------------------------------------------------------------
# Download and verify
# ---------------------------------------------------------------------------

resolve_tag() {
	if [ -n "$WANT_VERSION" ]; then
		TAG="$WANT_VERSION"
		return 0
	fi
	# Read the tag from where github.com redirects /releases/latest rather
	# than from the API. The API allows sixty unauthenticated calls an hour
	# per address, and a university behind one address burns that between
	# lectures; this is the same host the download comes from, so it adds
	# nothing new that can be unreachable.
	resolve_final=$(http_final_url "$BASE_URL/latest")
	case "$resolve_final" in
	*/releases/tag/*) TAG="${resolve_final##*/releases/tag/}" ;;
	*) TAG='' ;;
	esac
	return 0
}

asset_base() {
	# With no tag — wget, or a redirect that did not parse — the unversioned
	# path still resolves to the newest release. Only the printed version is
	# lost.
	if [ -n "$TAG" ]; then
		printf '%s\n' "$BASE_URL/download/$TAG"
	else
		printf '%s\n' "$BASE_URL/latest/download"
	fi
}

verify_checksum() {
	# The release lists every asset and we downloaded one of them, so
	# `sha256sum -c` would call the other six missing and fail. Its
	# --ignore-missing is not old enough to rely on where a Mac's shasum is
	# concerned, so pull out the one line that matters instead.
	verify_want=$(awk -v name="$ASSET" '
		$2 == name || $2 == "*" name { print $1; found = 1; exit }
		END { if (!found) exit 1 }
	' "$1") || verify_want=''

	if [ -z "$verify_want" ]; then
		die config "SHA256SUMS does not list $ASSET." \
			"That release does not appear to include a build for this machine.
Built targets: $SUPPORTED"
	fi

	# A redirect or an error page saved as the checksum file must never reach
	# the comparison below looking like a hash.
	if [ "${#verify_want}" -ne 64 ]; then
		die config "The checksum file could not be read as checksums." \
			"Downloading it again usually settles this. If it keeps happening,
report it at https://github.com/$REPO/issues"
	fi
	case "$verify_want" in
	*[!0-9a-f]*)
		die config "The checksum file could not be read as checksums." \
			"Downloading it again usually settles this. If it keeps happening,
report it at https://github.com/$REPO/issues"
		;;
	esac

	verify_got=$(sha256_of "$2") || verify_got=''
	if [ -z "$verify_got" ]; then
		die filesystem "No SHA-256 tool was found, so the download cannot be checked." \
			"Install one of sha256sum (coreutils), shasum or openssl, or re-run
with --skip-checksum to accept the download unverified."
	fi
	if [ "$verify_want" != "$verify_got" ]; then
		die config "$ASSET does not match the checksum the release published." \
			"Nothing was installed. The download was corrupted in transit, or
the file is not the one that release built.

  expected  $verify_want
  got       $verify_got"
	fi
}

download_binary() {
	download_base=$(asset_base)

	# Never HEAD a release asset to see whether it is there: the redirect ends
	# at a presigned URL signed for GET, and a HEAD comes back 401 whether the
	# file exists or not. A missing asset 404s on the GET.
	if ! http_get "$download_base/$ASSET" "$TMP_DIR/$ASSET"; then
		die network "Could not download $ASSET." \
			"Either that release has no build for this machine, or the network
refused the request. The releases are listed at
https://github.com/$REPO/releases"
	fi

	if [ "$SKIP_CHECKSUM" = yes ]; then
		warn "installing without checking SHA256SUMS, because --skip-checksum was passed"
		return 0
	fi

	if ! http_get "$download_base/SHA256SUMS" "$TMP_DIR/SHA256SUMS" quiet; then
		die config "That release publishes no SHA256SUMS, so the download cannot be checked." \
			"Releases before $FIRST_CHECKSUMMED were published without one.
Install a current release, or re-run with --skip-checksum to accept the
download unverified."
	fi
	verify_checksum "$TMP_DIR/SHA256SUMS" "$TMP_DIR/$ASSET"
	step checksum ok
}

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------

place_binary() {
	chmod 755 "$TMP_DIR/$ASSET"
	# Same filesystem as the temp directory on purpose, so this is a rename
	# rather than a copy — the idiom the tool itself uses for everything
	# durable it writes. A rename also replaces a running binary cleanly.
	mv -f "$TMP_DIR/$ASSET" "$TARGET"
}

link_binary() {
	if [ "$BIN_DIR" = "$INSTALL_DIR" ]; then
		return 0
	fi
	# `ln -sf` onto a real directory makes the link *inside* it, and `ln -sfn`
	# quietly does nothing and still exits 0. `ln -sfT` would say so but is
	# GNU-only, and this has to work on a Mac. So look first.
	if [ -d "$LINK" ] && [ ! -L "$LINK" ]; then
		die filesystem "$LINK is a directory." \
			"Move or remove it, then run the installer again."
	fi
	rm -f "$LINK"
	if ! ln -s "$TARGET" "$LINK"; then
		die filesystem "Could not link $LINK." \
			"Check that $BIN_DIR is writable by you."
	fi
}

do_install() {
	say ''
	say 'lms-sync installer'
	say ''

	resolve_dirs
	if [ "$ADOPTED" = yes ]; then
		step found "$INSTALL_DIR"
		step '' 'upgrading it in place, so config.toml and manifest.json stay put'
	fi

	trap cleanup EXIT INT TERM HUP
	mkdir -p "$INSTALL_DIR" || die filesystem "Could not create $INSTALL_DIR." \
		"Check that you can write there, or pass --install-dir."
	mkdir -p "$BIN_DIR" || die filesystem "Could not create $BIN_DIR." \
		"Check that you can write there, or pass --bin-dir."

	# lms-sync *writes* config.toml next to its binary, so an install into a
	# folder this user cannot write to fails much later, when the interface
	# tries to save settings and cannot say why.
	if [ ! -w "$INSTALL_DIR" ]; then
		die filesystem "$INSTALL_DIR is not writable by you." \
			"lms-sync keeps config.toml and manifest.json beside its binary, so
this folder has to stay writable. Pass --install-dir to choose another."
	fi

	# The temp directory goes inside the install directory, not /tmp: /tmp is
	# often a different filesystem (and always is on Termux), which would make
	# the move at the end a copy rather than a rename.
	TMP_DIR="$INSTALL_DIR/.install.$$"
	mkdir -p "$TMP_DIR"

	resolve_tag
	step target "$OS/$ARCH"
	if [ -n "$TAG" ]; then
		step version "$TAG"
	else
		step version 'latest'
	fi

	step downloading "$ASSET"
	download_binary
	place_binary
	step installed "$TARGET"

	if [ "$BIN_DIR" = "$INSTALL_DIR" ]; then
		step 'on path' "$INSTALL_DIR (the folder itself)"
	else
		link_binary
		step linked "$LINK"
	fi

	if path_has "$BIN_DIR"; then
		:
	elif [ "$MODIFY_PATH" != yes ]; then
		step path "not changed, because --no-modify-path was passed"
	else
		apply_to_profiles add_path_block
	fi

	say ''
	if ! "$TARGET" --version; then
		die filesystem "The installed binary would not run." \
			"That usually means the download is for a different machine than
this one. Report it at https://github.com/$REPO/issues"
	fi
	say ''

	if ! path_has "$BIN_DIR"; then
		say "Open a new terminal, or run this once in this one:"
		say ""
		say "  export PATH=\"$BIN_DIR:\$PATH\""
		say ""
	fi
	say "Run lms-sync to open the interface."
	say ""
	# The tool resolves a relative destination against the folder its config
	# is in, and ships with the relative default "Courses" — so until a
	# destination is set, coursework lands inside the install directory. Say
	# so here rather than leaving someone to find a library in a dot folder.
	say "Settings live in $INSTALL_DIR. Set a destination in the interface —"
	say "until you do, courses land in $INSTALL_DIR/Courses."
	say ""
}

# ---------------------------------------------------------------------------
# Uninstall
# ---------------------------------------------------------------------------

do_uninstall() {
	say ''
	say 'lms-sync installer — removing'
	say ''

	resolve_dirs

	if [ -L "$LINK" ] || [ -f "$LINK" ]; then
		rm -f "$LINK"
		step removed "$LINK"
	fi
	if [ -f "$TARGET" ]; then
		rm -f "$TARGET"
		step removed "$TARGET"
	fi

	apply_to_profiles remove_path_block

	# rmdir, never `rm -rf`. It fails on a directory that still has anything
	# in it, and that is exactly the guard wanted here: this can physically
	# not delete config.toml, a Google token, or a synced library. What is
	# left is listed instead, and deleting it stays the student's decision.
	if [ ! -d "$INSTALL_DIR" ]; then
		say ''
		say 'Removed.'
		say ''
		return 0
	fi
	if rmdir "$INSTALL_DIR" 2>/dev/null; then
		step removed "$INSTALL_DIR"
		say ''
		say 'Removed.'
		say ''
		return 0
	fi

	say ''
	say "The folder is still there, because it is not empty:"
	say ''
	say "  $INSTALL_DIR"
	for uninstall_left in "$INSTALL_DIR"/* "$INSTALL_DIR"/.[!.]*; do
		if [ ! -e "$uninstall_left" ]; then
			continue
		fi
		case "$(basename "$uninstall_left")" in
		config.toml) say "    config.toml     your LMS username and password" ;;
		manifest.json) say "    manifest.json   what has already been downloaded" ;;
		drive-token.json) say "    drive-token.json  your Google sign-in" ;;
		drive-push.json) say "    drive-push.json   what has been copied to Drive" ;;
		*) say "    $(basename "$uninstall_left")" ;;
		esac
	done
	say ''
	say 'Delete it yourself once you are sure:'
	say ''
	say "  rm -rf $INSTALL_DIR"
	say ''
}

# ---------------------------------------------------------------------------

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--version)
			shift
			if [ $# -eq 0 ]; then
				die config "--version needs a release tag." "For example: --version v1.3.0"
			fi
			WANT_VERSION=$(normalise_version "$1")
			;;
		--version=*) WANT_VERSION=$(normalise_version "${1#*=}") ;;
		--install-dir)
			shift
			if [ $# -eq 0 ]; then
				die config "--install-dir needs a path." "For example: --install-dir ~/lms-sync"
			fi
			INSTALL_DIR_OPT="$1"
			;;
		--install-dir=*) INSTALL_DIR_OPT="${1#*=}" ;;
		--bin-dir)
			shift
			if [ $# -eq 0 ]; then
				die config "--bin-dir needs a path." "For example: --bin-dir ~/.local/bin"
			fi
			BIN_DIR_OPT="$1"
			;;
		--bin-dir=*) BIN_DIR_OPT="${1#*=}" ;;
		--no-modify-path) MODIFY_PATH=no ;;
		--skip-checksum) SKIP_CHECKSUM=yes ;;
		--uninstall) ACTION=uninstall ;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			die config "Unknown option: $1" "Run with --help to see what this takes."
			;;
		esac
		shift
	done

	if [ "$WANT_VERSION" = v ]; then
		die config "--version needs a release tag." "For example: --version v1.3.0"
	fi
}

main() {
	parse_args "$@"

	if have curl; then
		HTTP_CLIENT=curl
	elif have wget; then
		HTTP_CLIENT=wget
	else
		HTTP_CLIENT=none
	fi

	detect_platform

	if [ "$ACTION" = uninstall ]; then
		do_uninstall
		return 0
	fi

	if [ "$HTTP_CLIENT" = none ]; then
		die config "Neither curl nor wget is installed, so nothing can be downloaded." \
			"Install one of them and run this again. On Termux: pkg install curl"
	fi
	do_install
}

# Called on the last line so that a download cut short part-way through —
# which is what `curl | sh` does when the connection drops — cannot run a
# half-written script. Everything above is a definition.
main "$@"
