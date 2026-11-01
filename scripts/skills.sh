#!/bin/sh
# The skills the repo carries (skills/<runtime>/<name>/) and the copies the runtimes load.
#
#   skills.sh check     compares each skill with its installed copy; touches nothing
#   skills.sh install   copies the repo's skills into the runtimes that load them
#
# Run through `make skills-check` and `make install-skills`. POSIX sh, for macOS and
# Linux: on Windows copy the folders by hand.
#
# What it promises, and the whole of what it does to the machine:
#   - it only ever touches the folders of the skills the repo has; every other folder
#     of a runtime's skills directory is the user's and is not read, listed or changed;
#   - it never deletes: a file that is in the installed copy and not in the repo is
#     listed and left alone;
#   - a copy that differs from the repo's is never overwritten without FORCE=1, and with
#     it every overwritten file is named. A difference does not say which side is newer,
#     so it is the person who decides, with the list in front of them;
#   - all or nothing: if one copy would be refused, no skill is copied;
#   - a runtime that is not on this machine (no ~/.codex) is skipped, with a line.
#
# Environment: SKILLS_DIR (default skills), BIN_DIR (default bin, where the zips for
# Claude Desktop go), FORCE (1 to overwrite), HOME. File names with a newline in them are
# not supported.
set -eu

mode=${1:-}
case $mode in check | install) ;; *)
	echo "usage: skills.sh check|install" >&2
	exit 2
	;;
esac

skills_dir=${SKILLS_DIR:-skills}
bin_dir=${BIN_DIR:-bin}
home=${HOME:?skills: HOME is not set}
force=${FORCE:-}

runtimes="claude codex antigravity claude-desktop"

# The directory a runtime keeps its configuration in. If it does not exist the runtime
# is not installed here.
runtime_root() {
	case $1 in
	claude) echo "$home/.claude" ;;
	codex) echo "$home/.codex" ;;
	antigravity) echo "$home/.gemini/config" ;;
	esac
}

# Claude Desktop has no folder to copy into: it takes a zip uploaded by hand.
installed_copy() {
	echo "$(runtime_root "$1")/skills/$2"
}

# One line per finding between a skill of the repo ($1) and its installed copy ($2):
#   D file   in both, and different
#   M file   in the repo, not in the installed copy
#   X file   in the installed copy, not in the repo (never a difference: it is left alone)
compare() {
	(cd "$1" && find . -type f ! -name .DS_Store | sed 's|^\./||' | sort) | while IFS= read -r f; do
		if [ ! -e "$2/$f" ]; then
			printf 'M %s\n' "$f"
		elif ! cmp -s "$1/$f" "$2/$f"; then
			printf 'D %s\n' "$f"
		fi
	done
	(cd "$2" && find . -type f ! -name .DS_Store | sed 's|^\./||' | sort) | while IFS= read -r f; do
		[ -e "$1/$f" ] || printf 'X %s\n' "$f"
	done
}

status_of() {
	if [ ! -d "$2" ]; then
		echo missing
	elif [ -n "$(compare "$1" "$2" | grep -v '^X ' || true)" ]; then
		echo different
	else
		echo ok
	fi
}

show_findings() {
	compare "$1" "$2" | while IFS= read -r line; do
		file=${line#? }
		case $line in
		D\ *) printf '    differs: %s\n' "$file" ;;
		M\ *) printf '    missing in the installed copy: %s\n' "$file" ;;
		X\ *) printf '    only in the installed copy, left alone: %s\n' "$file" ;;
		esac
	done
}

show_extras() {
	compare "$1" "$2" | while IFS= read -r line; do
		case $line in X\ *) printf '    only in the installed copy, left alone: %s\n' "${line#? }" ;; esac
	done
}

# Calls $1 for every skill of the repo with: status runtime name source installed-copy.
# The status is skipped (the runtime is not here), manual (Claude Desktop), or what
# status_of says.
each_skill() {
	for rdir in "$skills_dir"/*/; do
		[ -d "$rdir" ] || continue
		runtime=$(basename "$rdir")
		case " $runtimes " in
		*" $runtime "*) ;;
		*)
			echo "skills: $rdir is not a runtime this knows ($runtimes)" >&2
			exit 1
			;;
		esac
		for sdir in "$rdir"*/; do
			[ -d "$sdir" ] || continue
			src=${sdir%/}
			name=$(basename "$src")
			if [ "$runtime" = claude-desktop ]; then
				"$1" manual "$runtime" "$name" "$src" ""
				continue
			fi
			if [ ! -d "$(runtime_root "$runtime")" ]; then
				"$1" skipped "$runtime" "$name" "$src" ""
				continue
			fi
			dst=$(installed_copy "$runtime" "$name")
			"$1" "$(status_of "$src" "$dst")" "$runtime" "$name" "$src" "$dst"
		done
	done
}

[ -d "$skills_dir" ] || {
	echo "skills: $skills_dir is not a directory" >&2
	exit 1
}

# ---- check ------------------------------------------------------------------

bad=0
report() {
	case $1 in
	skipped) printf 'skipped    %s/%s  (%s does not exist)\n' "$2" "$3" "$(runtime_root "$2")" ;;
	manual) printf 'manual     %s/%s  (Claude Desktop takes a zip uploaded by hand: make install-skills builds %s/%s.zip)\n' "$2" "$3" "$bin_dir" "$3" ;;
	ok)
		printf 'ok         %s/%s\n' "$2" "$3"
		show_extras "$4" "$5"
		;;
	missing)
		printf 'missing    %s/%s  (%s)\n' "$2" "$3" "$5"
		bad=1
		;;
	different)
		printf 'different  %s/%s  (%s)\n' "$2" "$3" "$5"
		show_findings "$4" "$5"
		bad=1
		;;
	esac
}

if [ "$mode" = check ]; then
	each_skill report
	exit "$bad"
fi

# ---- install ----------------------------------------------------------------

conflicts=0
desktop=0
plan() {
	case $1 in
	different)
		conflicts=$((conflicts + 1))
		[ "$force" = 1 ] && return 0
		if [ "$conflicts" -eq 1 ]; then
			echo "refused: an installed copy differs from the repo's, and copying would overwrite it. Nothing was copied." >&2
		fi
		printf '  %s/%s  (%s)\n' "$2" "$3" "$5" >&2
		show_findings "$4" "$5" >&2
		;;
	manual) desktop=$((desktop + 1)) ;;
	esac
}

each_skill plan
if [ "$conflicts" -gt 0 ] && [ "$force" != 1 ]; then
	echo "  Run 'make skills-check' to see them again. A difference does not say which side is newer: if the repo's copy is the one to keep, run again with FORCE=1, which prints every file it overwrites." >&2
	exit 1
fi
if [ "$desktop" -gt 0 ] && ! command -v zip >/dev/null 2>&1; then
	echo "skills: zip is not installed, and Claude Desktop takes its skills as a zip. Nothing was copied." >&2
	exit 1
fi

zip_skill() {
	mkdir -p "$bin_dir"
	out=$(cd "$bin_dir" && pwd)/$2.zip
	rm -f "$out"
	(cd "$(dirname "$1")" && zip -q -r "$out" "$(basename "$1")")
	printf 'zipped     claude-desktop/%s -> %s\n' "$2" "$out"
}

apply() {
	case $1 in
	skipped) printf 'skipped    %s/%s  (%s does not exist)\n' "$2" "$3" "$(runtime_root "$2")" ;;
	manual) zip_skill "$4" "$3" ;;
	ok) printf 'unchanged  %s/%s\n' "$2" "$3" ;;
	missing)
		mkdir -p "$5"
		cp -R "$4/." "$5/"
		printf 'installed  %s/%s -> %s\n' "$2" "$3" "$5"
		;;
	different)
		compare "$4" "$5" | while IFS= read -r line; do
			case $line in D\ * | M\ *) printf 'overwrote  %s/%s: %s\n' "$2" "$3" "${line#? }" ;; esac
		done
		cp -R "$4/." "$5/"
		show_extras "$4" "$5"
		;;
	esac
}

each_skill apply
if [ "$desktop" -gt 0 ]; then
	echo "Claude Desktop: upload each zip by hand, in Customize > Skills > Upload a skill."
fi
