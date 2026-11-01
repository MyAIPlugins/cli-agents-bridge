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
#   - a copy that was edited since the last install is never overwritten without FORCE=1,
#     and with it every overwritten file is named. A difference from the repo does not
#     say which side is newer, so each install leaves a record of what it installed
#     (.cab-installed, in the skill's folder): a copy that still matches its record is
#     only behind the repo and is updated; one that does not match it was edited; one
#     with no record (never installed by this, or put there by hand) is taken as edited;
#   - it never writes through a symbolic link: a skill's folder, or anything inside the
#     copy, that is a link is refused with its path (FORCE does not override it), and so
#     is a link inside the repo's skill. The `skills` folder of a runtime may itself be a
#     link: that is the user's own arrangement, and what is under it is the skill's folder;
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

# The record of the last install, in the installed copy of a skill: one line per file,
# "<crc> <size> <path>" as POSIX cksum says it. It tells an edit by hand from a copy that
# is merely behind the repo. It is not a security control (cksum catches an edit, not
# someone hiding one), and it is not part of any comparison.
RECORD=.cab-installed

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

# Every symbolic link at $1 or inside it, one path per line. A link is neither followed
# nor looked inside.
links_in() {
	if [ -L "$1" ]; then
		echo "$1"
	elif [ -d "$1" ]; then
		find "$1" -type l
	fi
	return 0
}

# The files of a skill, as paths relative to its folder, sorted. The record is not one.
files_of() {
	(cd "$1" && find . -type f ! -name .DS_Store ! -name "$RECORD" ! -name "$RECORD.tmp" | sed 's|^\./||' | sort)
}

# One line per finding between a skill of the repo ($1) and its installed copy ($2):
#   D file   in both, and different
#   M file   in the repo, not in the installed copy
#   X file   in the installed copy, not in the repo (never a difference: it is left alone)
compare() {
	files_of "$1" | while IFS= read -r f; do
		if [ ! -e "$2/$f" ]; then
			printf 'M %s\n' "$f"
		elif ! cmp -s "$1/$f" "$2/$f"; then
			printf 'D %s\n' "$f"
		fi
	done
	files_of "$2" | while IFS= read -r f; do
		[ -e "$1/$f" ] || printf 'X %s\n' "$f"
	done
}

# The record a skill of the repo gives its copy when it is installed.
manifest() {
	files_of "$1" | while IFS= read -r f; do
		printf '%s %s\n' "$(cksum <"$1/$f")" "$f"
	done
}

# Replaced whole or not at all, so a record cut short cannot vouch for files it no
# longer lists.
write_record() {
	manifest "$1" >"$2/$RECORD.tmp"
	mv -f "$2/$RECORD.tmp" "$2/$RECORD"
}

# The files of an installed copy that are no longer what its record says, one per line.
# A file the record lists and the copy no longer has is one of them.
edited_since_install() {
	while IFS= read -r line; do
		crc=${line%% *}
		rest=${line#* }
		size=${rest%% *}
		path=${rest#* }
		if [ ! -f "$1/$path" ] || [ "$(cksum <"$1/$path")" != "$crc $size" ]; then
			echo "$path"
		fi
	done <"$1/$RECORD"
}

# ok       the copy is the repo's (files only in the copy do not count)
# missing  the skill is not installed
# behind   it differs from the repo and matches its record: only the repo moved
# edited   it differs from the repo and does not match its record, or has none
status_of() {
	if [ ! -d "$2" ]; then
		echo missing
	elif [ -z "$(compare "$1" "$2" | grep -v '^X ' || true)" ]; then
		echo ok
	elif [ -f "$2/$RECORD" ] && [ -z "$(edited_since_install "$2")" ]; then
		echo behind
	else
		echo edited
	fi
}

show_why_edited() {
	if [ ! -f "$1/$RECORD" ]; then
		echo "    no record of the last install, so the copy is taken as edited"
		return 0
	fi
	edited_since_install "$1" | while IFS= read -r f; do
		printf '    changed since the last install: %s\n' "$f"
	done
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
# The status is skipped (the runtime is not here), manual (Claude Desktop), linked (a
# symbolic link in the repo's skill or in the installed copy; the sixth argument lists
# them), or what status_of says.
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
				links=$(links_in "$src")
				if [ -n "$links" ]; then
					"$1" linked "$runtime" "$name" "$src" "" "$links"
				else
					"$1" manual "$runtime" "$name" "$src" ""
				fi
				continue
			fi
			if [ ! -d "$(runtime_root "$runtime")" ]; then
				"$1" skipped "$runtime" "$name" "$src" ""
				continue
			fi
			dst=$(installed_copy "$runtime" "$name")
			links=$(links_in "$src"; links_in "$dst")
			if [ -n "$links" ]; then
				"$1" linked "$runtime" "$name" "$src" "$dst" "$links"
				continue
			fi
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
	linked)
		printf 'linked     %s/%s  (a symbolic link: nothing is read or written through it)\n' "$2" "$3"
		printf '    %s\n' "$6"
		bad=1
		;;
	behind)
		printf 'different  %s/%s  (behind the repo; %s)\n' "$2" "$3" "$5"
		show_findings "$4" "$5"
		bad=1
		;;
	edited)
		printf 'different  %s/%s  (locally modified; %s)\n' "$2" "$3" "$5"
		show_why_edited "$5"
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
linked=0
plan() {
	case $1 in
	linked)
		linked=$((linked + 1))
		if [ "$linked" -eq 1 ]; then
			echo "refused: a symbolic link is in the way, and copying would write through it, outside the folder of the skill. FORCE does not change that. Nothing was copied." >&2
		fi
		printf '  %s/%s\n' "$2" "$3" >&2
		printf '    %s\n' "$6" >&2
		;;
	edited)
		conflicts=$((conflicts + 1))
		[ "$force" = 1 ] && return 0
		if [ "$conflicts" -eq 1 ]; then
			echo "refused: an installed copy was edited since the last install, and copying would overwrite the edit. Nothing was copied." >&2
		fi
		printf '  %s/%s  (%s)\n' "$2" "$3" "$5" >&2
		show_why_edited "$5" >&2
		show_findings "$4" "$5" >&2
		;;
	manual) desktop=$((desktop + 1)) ;;
	esac
}

each_skill plan
if [ "$linked" -gt 0 ]; then
	exit 1
fi
if [ "$conflicts" -gt 0 ] && [ "$force" != 1 ]; then
	echo "  Run 'make skills-check' to see them again. If the repo's copy is the one to keep, run again with FORCE=1, which prints every file it overwrites." >&2
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

# One line per file the install is about to replace: $1 is the word for it, then the
# skill's runtime and name, its folder in the repo and its installed copy.
say_replaced() {
	compare "$4" "$5" | while IFS= read -r line; do
		case $line in D\ * | M\ *) printf '%-11s%s/%s: %s\n' "$1" "$2" "$3" "${line#? }" ;; esac
	done
}

apply() {
	case $1 in
	skipped) printf 'skipped    %s/%s  (%s does not exist)\n' "$2" "$3" "$(runtime_root "$2")" ;;
	manual) zip_skill "$4" "$3" ;;
	ok)
		# Identical to the repo's, so it is certain nothing was edited: that is what
		# the record is for, and it is written when it is missing or stale.
		if [ -f "$5/$RECORD" ] && manifest "$4" | cmp -s - "$5/$RECORD"; then
			printf 'unchanged  %s/%s\n' "$2" "$3"
		else
			write_record "$4" "$5"
			printf 'adopted    %s/%s  (identical to the repo: recorded as installed)\n' "$2" "$3"
		fi
		;;
	missing)
		mkdir -p "$5"
		cp -R "$4/." "$5/"
		write_record "$4" "$5"
		printf 'installed  %s/%s -> %s\n' "$2" "$3" "$5"
		;;
	behind)
		say_replaced updated "$2" "$3" "$4" "$5"
		cp -R "$4/." "$5/"
		write_record "$4" "$5"
		show_extras "$4" "$5"
		;;
	edited)
		say_replaced overwrote "$2" "$3" "$4" "$5"
		cp -R "$4/." "$5/"
		write_record "$4" "$5"
		show_extras "$4" "$5"
		;;
	esac
}

each_skill apply
if [ "$desktop" -gt 0 ]; then
	echo "Claude Desktop: upload each zip by hand, in Customize > Skills > Upload a skill."
fi
