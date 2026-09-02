package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMakefile_LintLooksWhereGoInstallPuts is the regression for a remediation
// that could not fix what it diagnosed.
//
// `make lint` requires staticcheck and prints an install command when it is
// missing. The first version resolved the binary in $PATH and $GOPATH/bin only
// — but `go install` writes to $GOBIN whenever that is set, so on such a machine
// the gate looked where the binary is not, and then told the reader to install
// it there again. Fail-closed, so nothing unsafe was authorised: a deterministic
// false failure with a remediation that loops.
//
// Third instance of that class in one day, after a repair command that repaired
// a different project and an error telling a peer to quote something while
// showing an unquotable form. A suggested command is a claim like any other, and
// the way to check it is to run it and see whether the problem goes away.
//
// The assertion is on WHICH binary is chosen, not on the exit code: a gate that
// happens to fail for the right reason today is not the property being pinned.
func TestMakefile_LintLooksWhereGoInstallPuts(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	// A fake staticcheck ONLY under GOBIN, with GOPATH pointing somewhere that
	// has none — so the two candidate directories give different answers and the
	// test can tell which one the Makefile used.
	tmp := t.TempDir()
	gobin := filepath.Join(tmp, "gobin")
	gopath := filepath.Join(tmp, "gopath")
	require.NoError(t, os.MkdirAll(gobin, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(gopath, "bin"), 0o700))
	fake := filepath.Join(gobin, "staticcheck")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\necho FAKE_STATICCHECK\n"), 0o700))
	// One under GOPATH/bin too. It used to be enough to leave that directory
	// empty and assert that the Makefile NAMED it, because with PATH consulted
	// last the name is only reached when a binary is actually there — asserting
	// on the name no longer separates "chose GOPATH/bin" from "found nothing
	// anywhere and printed the install hint".
	gopathFake := filepath.Join(gopath, "bin", "staticcheck")
	require.NoError(t, os.WriteFile(gopathFake, []byte("#!/bin/sh\necho GOPATH_STATICCHECK\n"), 0o700))

	// A THIRD staticcheck, at the head of PATH, and it is the whole reason this
	// test was green on a Mac while red in CI for three weeks.
	//
	// The Makefile resolved $PATH FIRST and GOBIN second, contradicting both its
	// own comment and the two subtests below. On a developer machine staticcheck
	// is not in PATH, so the two orders give the same answer and nothing shows.
	// On the runner `setup-go` puts /home/runner/go/bin in PATH and the CI step
	// installs staticcheck into it — so `command -v` won the race, the planted
	// fakes were never consulted, and the assertions failed on an environment the
	// test never created for itself.
	//
	// Planting one here reproduces the runner on any machine: the test is now red
	// without the Makefile fix, which is the only way it can be a regression.
	pathDir := filepath.Join(tmp, "pathbin")
	require.NoError(t, os.MkdirAll(pathDir, 0o700))
	inPath := filepath.Join(pathDir, "staticcheck")
	require.NoError(t, os.WriteFile(inPath, []byte("#!/bin/sh\necho PATH_STATICCHECK\n"), 0o700))

	run := func(env ...string) string {
		t.Helper()
		cmd := exec.Command("make", "-n", "lint")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), env...)
		cmd.Env = append(cmd.Env, "PATH="+pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, _ := cmd.CombinedOutput() // -n prints, it does not run: exit code is not the subject
		return string(out)
	}

	t.Run("GOBIN set wins, because that is where go install writes", func(t *testing.T) {
		out := run("GOBIN="+gobin, "GOPATH="+gopath)
		assert.Contains(t, out, fake,
			"with GOBIN set the gate must look there — otherwise its own install command cannot fix it")
		assert.NotContains(t, out, gopathFake,
			"and must not fall back to GOPATH/bin, which is not where go install wrote on such a machine")
		assert.NotContains(t, out, inPath,
			"nor to a staticcheck that merely happens to be in PATH: the pin governs the binary that RUNS "+
				"only if the Makefile runs the one `go install` wrote")
	})

	t.Run("GOBIN empty falls back to GOPATH/bin", func(t *testing.T) {
		out := run("GOBIN=", "GOPATH="+gopath)
		assert.Contains(t, out, gopathFake,
			"the ordinary machine, and the behaviour that must not regress")
		assert.NotContains(t, out, inPath, "PATH is the fallback, not the first choice")
	})

	// And the branch next door, which the reordering must NOT break: a machine
	// where staticcheck exists only in PATH — installed by a package manager, or
	// by a CI step into a directory that is not this GOBIN — must still find it.
	// Demoting PATH from first to last is not the same as removing it.
	t.Run("PATH is still the fallback when go install put one nowhere", func(t *testing.T) {
		bare := filepath.Join(tmp, "bare")
		require.NoError(t, os.MkdirAll(filepath.Join(bare, "bin"), 0o700))
		out := run("GOBIN=", "GOPATH="+bare)
		assert.Contains(t, out, inPath,
			"with neither GOBIN nor GOPATH/bin holding one, the gate must still use what is in PATH")
	})

	t.Run("the pinned version is one file, and NOTHING repeats the number", func(t *testing.T) {
		// The first version of this subtest asserted that CI *reads* the file and
		// that no `staticcheck@v...` literal appears on a command line. Both were
		// true, and the property claimed by the commit — one source — was still
		// false: the number was also sitting in a COMMENT in the workflow.
		//
		// The critic proved it the only way that works for a "single source":
		// change the value in the declared source and see what stays behind. The
		// subtest was green with the file saying v9.9.9 and the workflow still
		// announcing the old number. Proving a mechanism EXISTS does not prove it
		// is the only one, and for a single source it is the absence of
		// alternatives that has to be proven.
		raw, rerr := os.ReadFile(filepath.Join(repoRoot, ".staticcheck-version"))
		require.NoError(t, rerr, "the single source of the pin must exist")
		version := strings.TrimSpace(string(raw))
		require.Regexp(t, `^v\d+\.\d+\.\d+$`, version)

		ci, rerr := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
		require.NoError(t, rerr)
		assert.Contains(t, string(ci), ".staticcheck-version",
			"CI must READ the file rather than repeat the number")

		// ANY semver literal in the staticcheck STEP — comments included, since a
		// comment is exactly where the second copy hid. Scoped to the step rather
		// than the whole file: the first draft of this assertion swept the entire
		// Makefile and tripped on `git checkout v0.2.3`, a project tag in an
		// unrelated example. A regression that cries about the wrong line gets
		// switched off, so it has to name its subject.
		semver := regexp.MustCompile(`v\d+\.\d+\.\d+`)
		assert.Empty(t, semver.FindAllString(stepNamed(t, string(ci), "staticcheck"), -1),
			"no version literal may appear in the staticcheck step, in code OR in a comment: "+
				"a number written 'for the reader' is a second source, and it is the one that goes stale")

		// Same rule on the Makefile, restricted to the lines that talk about it.
		mk, rerr := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
		require.NoError(t, rerr)
		for _, line := range strings.Split(string(mk), "\n") {
			if strings.Contains(strings.ToLower(line), "staticcheck") {
				assert.Empty(t, semver.FindAllString(line, -1),
					"the Makefile must derive the version, never restate it: %q", line)
			}
		}

		// LIMIT, stated rather than left to be assumed: this catches a literal in
		// the places the number has actually appeared. A semver written somewhere
		// that never mentions staticcheck would still slip through — the honest
		// guard against that is the critic's method, which is to change the value
		// in the source and see what stays behind.
	})

	// Resolving the right PATH is not the same as running the right binary: the
	// gate executed whatever it found. An outdated staticcheck then produced a
	// false green, a newer one a false red, while the commit claimed the version
	// was pinned in one place (CRI).
	t.Run("a staticcheck whose version is not the pinned one is REFUSED", func(t *testing.T) {
		pin := pinnedVersion(t, repoRoot)
		// The wrong versions are DERIVED from the pin rather than written down, so
		// they cannot accidentally become the pin the day someone bumps the file —
		// which would turn "refused" into "runs" and the test into a no-op.
		notThePin := pin + "1"
		for _, tc := range []struct {
			name, reports string
			wantRun       bool
		}{
			{"the pinned version runs", "staticcheck 2026.1 (" + pin + ")", true},
			{"a different one is refused", "staticcheck 2024.1 (" + notThePin + ")", false},
			{"one that cannot say is refused", "", false},
			{"one that answers nonsense is refused", "not a version at all", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				fake := filepath.Join(dir, "staticcheck")
				script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + tc.reports + "'; exit 0; fi\necho FAKE_LINT_RAN\n"
				require.NoError(t, os.WriteFile(fake, []byte(strings.ReplaceAll(script, "\\n", "\n")), 0o700))

				cmd := exec.Command("make", "lint", "STATICCHECK="+fake)
				cmd.Dir = repoRoot
				out, err := cmd.CombinedOutput()

				if tc.wantRun {
					// BOTH, and the exit code is not decoration: today the marker is
					// printed by the last recipe line, so a failure after it cannot
					// exist — add one recipe below staticcheck and this case would go
					// green on a failing `make lint`, with its own name saying the
					// opposite (CRI, P3).
					require.NoError(t, err, "the pinned version must run AND the target must succeed: %s", out)
					assert.Contains(t, string(out), "FAKE_LINT_RAN", "the pinned version must be executed")
					return
				}
				require.Error(t, err, "a mismatched staticcheck must not be run: %s", out)
				assert.Contains(t, string(out), "version mismatch")
				assert.NotContains(t, string(out), "FAKE_LINT_RAN",
					"and must not have been executed before the check")
			})
		}
	})
}

// TestMakefile_LintRunsFromAPathWithSpaces is F-124's own defect, found inside
// the Makefile written to verify F-124's fix.
//
// The existence guard quoted "$(STATICCHECK)"; the version probe and the
// invocation did not. So a GOBIN whose basename contains a space split the path:
// the correct binary — reporting exactly the pinned version — was never invoked,
// the gate read "unknown", refused it, and then advised reinstalling it in the
// same directory. The remediation loop, on another component of the path, two
// hours after closing it elsewhere.
//
// The assertion is EXIT 0 PLUS THE MARKER, not that `make -n` prints the path:
// choosing a binary is not running it, which is the distinction this whole lot
// keeps turning on.
func TestMakefile_LintRunsFromAPathWithSpaces(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	pin := pinnedVersion(t, repoRoot)

	// A space in the DIRECTORY, which is the half a quoted $(STATICCHECK) in one
	// place out of three did not survive.
	gobin := filepath.Join(t.TempDir(), "cri staticcheck space")
	require.NoError(t, os.MkdirAll(gobin, 0o700))
	fake := filepath.Join(gobin, "staticcheck")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'staticcheck 2026.1 (" + pin + ")'; exit 0; fi\n" +
		"echo MARKER_LINT_RAN\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o700))

	// A decoy in PATH, reporting a version that is NOT the pin, for the same
	// reason as in the test above: on a developer machine staticcheck is absent
	// from PATH, so a Makefile that consulted PATH first would still pick the
	// GOBIN one here and this test would pass while CI failed. With the decoy the
	// wrong order produces "version mismatch" instead of MARKER_LINT_RAN, and the
	// test can fail for the reason it is named after.
	decoyDir := filepath.Join(t.TempDir(), "decoy")
	require.NoError(t, os.MkdirAll(decoyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(decoyDir, "staticcheck"),
		[]byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'staticcheck 1999.1 (v0.0.1)'; exit 0; fi\necho DECOY_LINT_RAN\n"), 0o700))

	cmd := exec.Command("make", "lint")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOBIN="+gobin, "PATH="+decoyDir+":/usr/bin:/bin:"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()

	require.NoError(t, err, "a correct staticcheck under a path with a space must not be refused: %s", out)
	assert.Contains(t, string(out), "MARKER_LINT_RAN", "and it must actually be RUN, not merely resolved")
	assert.NotContains(t, string(out), "unknown",
		"the version probe must reach the binary too — reading 'unknown' here is the path having been split")
	assert.NotContains(t, string(out), "version mismatch",
		"refusing the right binary and then advising to reinstall it in the same place is the loop we keep closing")
}

func pinnedVersion(t *testing.T, repoRoot string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".staticcheck-version"))
	require.NoError(t, err)
	return strings.TrimSpace(string(raw))
}

// stepNamed returns the YAML block of a workflow step, from its `- name: <n>`
// line to the next step at the same indentation — so an assertion about "the
// staticcheck step" is about that step and not about the whole file.
func stepNamed(t *testing.T, yaml, name string) string {
	t.Helper()
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "- name: ") && strings.Contains(l, name) {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "step %q not found in the workflow", name)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- name: ") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// TestMakefile_InstallDevLinksWhatItJustBuilt covers the command that puts
// cab-bridge on PATH — the only build artefact the agents actually use.
//
// Two defects in four lines, and the second is the worse one:
//
//   - the paths were unquoted, so a checkout under a directory with a space made
//     `ln` fail AFTER the build had succeeded;
//   - it used $(PWD), the CALLER's working directory, which Make does not
//     maintain. With `make -C <repo>` from elsewhere it built inside <repo> and
//     linked to <caller-cwd>/bin/cab-bridge — reproduced as a DANGLING symlink
//     installed into ~/.local/bin, with exit 0.
//
// "Merged is not installed" is a rule this project repeats; here the command
// that closes that distance could install the wrong binary and say nothing.
//
// One scenario catches both, and it has to: a repo whose path contains a space,
// invoked with `make -C` from a different directory. Asserting exit 0 would not
// be enough — the broken version exited 0 too — so the assertion is on where the
// symlink POINTS and whether that target runs.
func TestMakefile_InstallDevLinksWhatItJustBuilt(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	tmp := t.TempDir()
	repo := filepath.Join(tmp, "a repo with spaces")
	home := filepath.Join(tmp, "home")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	require.NoError(t, os.MkdirAll(home, 0o700))

	copyWorkingTree(t, repoRoot, repo)

	// Invoked with -C from a directory that is NOT the repo: the case where
	// $(PWD) and $(CURDIR) disagree, and the only one that tells them apart.
	cmd := exec.Command("make", "-C", repo, "install-dev")
	cmd.Dir = filepath.Join(repoRoot, "docs")
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "install-dev must survive a path with spaces: %s", out)

	link := filepath.Join(home, ".local", "bin", "cab-bridge")
	target, err := os.Readlink(link)
	require.NoError(t, err, "the symlink must exist: %s", out)

	// SYMLINK-RESOLVED on both sides: $(CURDIR) is canonical, while t.TempDir()
	// hands back /var/... which on macOS is a link to /private/var/.... Comparing
	// the raw strings would fail for a reason that has nothing to do with the
	// defect — the same care the scope tests already take.
	wantTarget, err := filepath.EvalSymlinks(filepath.Join(repo, "bin", "cab-bridge"))
	require.NoError(t, err, "the built binary must exist to be resolved")
	gotTarget, err := filepath.EvalSymlinks(target)
	require.NoError(t, err, "the symlink must not dangle: %s", target)
	assert.Equal(t, wantTarget, gotTarget,
		"it must point at the binary THIS invocation built, not at one under the caller's cwd")

	// And the target must actually be there: the broken version produced a link
	// to a path that did not exist, which no exit code revealed.
	info, err := os.Stat(gotTarget)
	require.NoError(t, err, "the symlink must not dangle")
	assert.NotZero(t, info.Mode()&0o111, "and what it points at must be executable")
}

// copyWorkingTree copies the source tree as it is RIGHT NOW into dst.
//
// Not `git archive HEAD`: that ships the last commit, so a regression for an
// uncommitted change runs against the code without the change — which is how the
// first version of this fixture went red against the defect it was removing.
//
// And not `git ls-files` alone either, which is what replaced it: that made the
// whole suite depend on a .git directory, and `Makefile:11-15` declares the
// no-git case SUPPORTED ("dev ... when not in a git repo, e.g. source tarball
// build"). From an extracted tarball the build worked and this test failed on
// valid sources — the fix to a tool opening the branch next door to the tool,
// third time on this Makefile (CRI).
//
// A t.Skip there would have been worse than the bug: green precisely where
// nobody verified anything, which is the class this whole arc has been closing.
//
// So: git when it is available, a plain walk otherwise. The two differ in one
// way worth stating — the walk also copies UNTRACKED files, since without git
// there is nothing to ask. Harmless for a fixture that only has to build.
func copyWorkingTree(t *testing.T, src, dst string) {
	t.Helper()

	// THE QUESTION IS "is src the ROOT of a repository", not "is src somewhere
	// inside one" — and --is-inside-work-tree answers the second.
	//
	// A source archive without .git, extracted inside another repository (vendor
	// source, a monorepo, a fixture in a workspace) IS inside a work tree, via
	// the parent. `git ls-files` there lists zero files, because the directory is
	// untracked in that parent — so the fixture came out EMPTY and make failed
	// with "No rule to make target `install-dev'". The test then failed on how
	// its container was classified, not on the thing it tests (CRI).
	//
	// --show-toplevel is the property that was meant: compare it with src, both
	// resolved, and take the Git branch only when they are the same directory.
	if isRepoRoot(src) {
		// --cached --others --exclude-standard: TRACKED plus UNTRACKED-not-ignored,
		// because "as it is RIGHT NOW" was the promise and --cached alone is "as it
		// was at the last `git add`".
		//
		// Found the way these are always found: by using it. A lot that adds new
		// files went red with `undefined: ownerCheckPath` — the fixture had copied
		// the MODIFIED perms.go, which was tracked, and not the new perms_unix.go
		// next to it, which was not. The build failed on a tree that has never
		// existed anywhere, and nothing in the output said so.
		//
		// It is the same class the comment above says it closed twice, and the
		// branch next door to both: `git archive HEAD` was the last COMMIT, this
		// was the last STAGE, and the walk below — the no-git path — has been
		// copying untracked files all along. Three ways to answer "what is in this
		// tree", and two of them were answering about a different moment.
		//
		// --exclude-standard keeps .gitignore honoured, so bin/ and the developer's
		// own scratch files stay out; a fixture that only has to build does not
		// want them.
		list := gitCommand(src, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		tracked, lerr := list.Output()
		require.NoError(t, lerr, "git ls-files")
		tarc := exec.Command("tar", "-c", "-f", "-", "--null", "-T", "-")
		tarc.Dir = src
		tarc.Stdin = bytes.NewReader(tracked)
		tarball, terr := tarc.Output()
		require.NoError(t, terr, "tar the working tree")
		untar := exec.Command("tar", "-x", "-C", dst)
		untar.Stdin = bytes.NewReader(tarball)
		require.NoError(t, untar.Run(), "untar into %s", dst)
		return
	}

	// No git: walk. Skips .git (absent anyway, but a bare copy may carry one),
	// bin/ (build output, rebuilt by the target under test), and dst itself in
	// case somebody points it inside the tree.
	require.NoError(t, filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || rel == "bin" || path == dst) {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if !info.Mode().IsRegular() {
			return nil // symlinks and friends: a build fixture does not need them
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if merr := os.MkdirAll(filepath.Dir(target), 0o755); merr != nil {
			return merr
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}), "copying the source tree without git")
}

// isRepoRoot reports whether dir is the top level of a Git work tree — not
// merely a directory somewhere beneath one.
//
// Both sides are symlink-resolved: git answers canonically, and on macOS a path
// under /var arrives as a link to /private/var, so a raw string comparison would
// say "not the root" about the root.
func isRepoRoot(dir string) bool {
	out, err := gitCommand(dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	self, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return top == self
}

// TestCopyWorkingTree_PicksTheBranchByROOTNotByContainment pins the
// discriminator, which is the part the previous two checks could not see.
//
// Both of them — mine and the val's — used a source tree in /tmp, outside any
// repository, so "is this a work tree?" and "is this THE root?" gave the same
// answer and the difference between them was invisible. A source archive dropped
// inside another repo separates the two: it is inside a work tree (the parent's)
// while being the root of nothing, `git ls-files` lists zero files there, and
// the fixture came out empty — make then failed with "No rule to make target",
// i.e. the test failing on how its container was classified.
func TestCopyWorkingTree_PicksTheBranchByROOTNotByContainment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	// A parent repository, and inside it a source tree that is NOT tracked and
	// has no .git of its own.
	parent := t.TempDir()
	init := exec.Command("git", "init", "-q")
	init.Dir = parent
	require.NoError(t, init.Run())

	nested := filepath.Join(parent, "source")
	require.NoError(t, os.MkdirAll(nested, 0o700))
	copyWorkingTree(t, repoRoot, nested)

	// The discriminator itself.
	assert.True(t, isRepoRoot(repoRoot), "the real checkout is a root")
	assert.False(t, isRepoRoot(nested),
		"a source tree inside another repo is INSIDE a work tree but is not its root — "+
			"telling those apart is the whole fix")

	// And the consequence that matters: the copy is usable. Empty is what the
	// broken guard produced, and `make` reported it as a missing target rather
	// than as a missing tree.
	for _, must := range []string{"Makefile", "go.mod", filepath.Join("cmd", "cab-bridge", "main.go")} {
		assert.FileExists(t, filepath.Join(nested, must),
			"%s must be in the copy — an empty fixture fails as 'No rule to make target'", must)
	}

	// Copying OUT of that nested tree must work too: it is the path the fallback
	// takes, and the one the previous version never reached.
	again := filepath.Join(t.TempDir(), "from nested")
	require.NoError(t, os.MkdirAll(again, 0o700))
	copyWorkingTree(t, nested, again)
	assert.FileExists(t, filepath.Join(again, "Makefile"),
		"the walk must carry the tree even when git would answer about somebody else's repository")
}

// TestCopyWorkingTree_CarriesUncommittedFiles is the regression for the fixture
// copying the wrong TREE — not the wrong directory, the wrong moment.
//
// `git ls-files` alone lists what is in the INDEX, so a file created and not yet
// committed was left out while its already-tracked neighbours came along. The
// copy then contained half a change: a modified file calling a function whose
// new file was missing, and `make build` failing inside the fixture on a state
// that exists nowhere. Nothing in the output points at the fixture, so the
// reader looks for the defect in their own code.
//
// The three cases are the contract in full: committed comes, uncommitted comes,
// ignored stays out. The middle one is the regression; the third is the branch
// next door — --others without --exclude-standard would have dragged in bin/ and
// every scratch file the developer has lying around.
func TestCopyWorkingTree_CarriesUncommittedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	src := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "fixture"},
	} {
		require.NoError(t, gitCommand(src, args...).Run(), "git %v", args)
	}

	require.NoError(t, os.WriteFile(filepath.Join(src, ".gitignore"), []byte("ignored.txt\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "committed.txt"), []byte("in the index"), 0o600))
	require.NoError(t, gitCommand(src, "add", ".").Run())
	require.NoError(t, gitCommand(src, "commit", "-q", "-m", "fixture").Run())

	// The two that are NOT in the index, one of each kind.
	require.NoError(t, os.WriteFile(filepath.Join(src, "uncommitted.txt"), []byte("written just now"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "ignored.txt"), []byte("build output"), 0o600))

	require.True(t, isRepoRoot(src), "the fixture must take the git branch, or this measures the walk instead")

	dst := t.TempDir()
	copyWorkingTree(t, src, dst)

	assert.FileExists(t, filepath.Join(dst, "committed.txt"), "tracked files must still be copied")
	assert.FileExists(t, filepath.Join(dst, "uncommitted.txt"),
		"a file written and not yet committed IS part of the tree as it is right now — "+
			"leaving it out builds a state that has never existed")
	assert.NoFileExists(t, filepath.Join(dst, "ignored.txt"),
		"and .gitignore is still honoured: --others without --exclude-standard would drag in bin/ too")
}

// gitCommand runs git in dir with the INHERITED GIT ENVIRONMENT STRIPPED.
//
// Sanitising only the selector would not have been enough, and that is the whole
// finding: `isRepoRoot` and `git ls-files` are two subprocesses, and the
// variables hit them differently. With GIT_DIR pointing at an unrelated
// repository, `rev-parse --show-toplevel` still answered with the RIGHT path —
// the selector passed — while `ls-files` queried the alien index and returned 0
// files instead of 157. Empty fixture, "No rule to make target 'install-dev'".
//
// AND IT IS NOT A LABORATORY CASE: git exports GIT_DIR into every hook. A
// pre-commit or pre-push that runs `make test` inherits it, and exec.Command
// hands it to its children. Whoever meets this is not building a scenario, they
// are committing.
//
// EVERY GIT_ VARIABLE, not a list of the ones that matter. A list is a whitelist
// of what I happened to think of, and today's lesson is that the one that gets
// you is the one you did not have in mind — the PATH while we were isolating the
// data dir, the environment while we were enumerating directories. The prefix
// costs nothing here: both commands are purely local queries.
//
// LIMIT, stated: this covers variables named GIT_*. Something like HOME still
// changes which global config git reads — it does not change WHICH REPOSITORY is
// queried, which is what this function is defending, but it is not "the git
// environment is neutralised" either.
func gitCommand(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	env := os.Environ()
	clean := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		clean = append(clean, kv)
	}
	cmd.Env = clean
	return cmd
}

// TestCopyWorkingTree_IgnoresAnInheritedGitEnvironment pins BOTH subprocesses,
// because fixing only the one that looked responsible would have left the defect
// in place.
//
// With GIT_DIR pointing at an unrelated repository the SELECTOR still answers
// correctly — `rev-parse --show-toplevel` returns the real root, so isRepoRoot
// says true — and the PRODUCER is the one that breaks: `ls-files` reads the
// alien index and lists nothing. The fixture comes out empty and make reports a
// missing target. Two commands, one environment, and only one of them visibly
// wrong.
//
// Reachable without trying: git exports GIT_DIR into every hook, so a pre-commit
// running `make test` inherits it.
func TestCopyWorkingTree_IgnoresAnInheritedGitEnvironment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	// An unrelated repository, with an index of its own that has nothing in it.
	alien := t.TempDir()
	initCmd := exec.Command("git", "init", "-q")
	initCmd.Dir = alien
	require.NoError(t, initCmd.Run())

	// The three do NOT behave alike, and saying they did was a generalisation
	// this test previously carried in an assertion message. Measured, on the
	// UNSANITISED command:
	//
	//	GIT_DIR         --show-toplevel = the real root  -> old selector TRUE
	//	GIT_INDEX_FILE  --show-toplevel = the real root  -> old selector TRUE
	//	GIT_WORK_TREE   --show-toplevel = the ALIEN      -> old selector FALSE
	//
	// So only the first two produce "selector green, producer empty", which is
	// the shape that made the defect survive a careful reading. GIT_WORK_TREE
	// would have sent the old code down the walk, which copies correctly — it was
	// never broken, and it is here to stay unbroken.
	//
	// Keeping the row and stating the difference beats dropping it: the value of
	// the case is that ONE environment variable out of three behaves differently,
	// and a reader who assumes they are interchangeable is the reader this
	// comment exists for.
	for _, env := range []struct {
		name, value      string
		oldSelectorFound bool // whether the pre-fix selector still saw the real root
	}{
		{"GIT_DIR", filepath.Join(alien, ".git"), true},
		{"GIT_INDEX_FILE", filepath.Join(alien, ".git", "index"), true},
		{"GIT_WORK_TREE", alien, false},
	} {
		t.Run(env.name, func(t *testing.T) {
			t.Setenv(env.name, env.value)

			// With the fix in place the selector is right for all three; the
			// column above records which ones it was ALREADY right about, and it
			// is the reason fixing the selector alone would not have been enough.
			assert.True(t, isRepoRoot(repoRoot),
				"%s must not stop the sanitised selector from recognising the root", env.name)

			// And the table above is CHECKED, not just written: run git the way
			// the pre-fix code did — inheriting the environment — and confirm
			// which variables still pointed at the real root. A column nobody
			// verifies is one more piece of prose that cannot fail.
			raw, rerr := exec.Command("git", "-C", repoRoot, "rev-parse", "--show-toplevel").Output()
			require.NoError(t, rerr)
			unsanitised, rerr := filepath.EvalSymlinks(strings.TrimSpace(string(raw)))
			require.NoError(t, rerr)
			realRoot, rerr := filepath.EvalSymlinks(repoRoot)
			require.NoError(t, rerr)
			assert.Equal(t, env.oldSelectorFound, unsanitised == realRoot,
				"%s: the pre-fix selector behaved as the table claims", env.name)

			dst := filepath.Join(t.TempDir(), "copy")
			require.NoError(t, os.MkdirAll(dst, 0o700))
			copyWorkingTree(t, repoRoot, dst)

			for _, must := range []string{"Makefile", "go.mod", filepath.Join("cmd", "cab-bridge", "main.go")} {
				assert.FileExists(t, filepath.Join(dst, must),
					"%s must not make the copy empty — an empty tree surfaces as 'No rule to make target'", env.name)
			}
		})
	}
}
