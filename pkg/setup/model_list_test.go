package setup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Model revisions plan, item C2 (second half): `flockadoo model list` and
// `flockadoo model verify`.
//
//   - list is cheap: it hashes nothing. For each catalog alias with a snapshot
//     in the cache it shows the commit, its size on disk, its status in the
//     catalog (read from the folder name, so a claim, not a check) and the
//     server's last verdict on it from the verdict log;
//   - verify measures one snapshot fresh, asks the server, and shows the
//     server's verdict; the client has no hashes to compare with. A snapshot
//     not in the cache is refused before anything is measured.

// weights puts a weight file of n bytes into a cached snapshot.
func (w *modelWorld) weights(t *testing.T, repo, sha string, n int) string {
	t.Helper()
	w.cache(t, repo, sha, "")
	p := filepath.Join(w.hub, verify.HFRepoToCacheDir(repo), "snapshots", sha, "model.safetensors")
	if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (w *modelWorld) judged(t *testing.T, sha string, status verify.Status, at time.Time) {
	t.Helper()
	if err := w.verdicts.Record(verify.VerdictRecord{Alias: "org/m", ServiceType: "vllm", Revision: sha, Status: status, At: at}); err != nil {
		t.Fatal(err)
	}
}

// line is the line of out that names sha.
func line(out, sha string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sha) {
			return l
		}
	}
	return ""
}

func TestModelListShowsEachCachedSnapshotWithItsStatusAndLastVerdict(t *testing.T) {
	w := newModelWorld(t)
	w.weights(t, "org/m", curSHA, 1_500_000)
	w.weights(t, "org/m", accSHA, 10)
	w.weights(t, "org/m", oldSHA, 10)
	at := time.Date(2026, 11, 24, 9, 12, 0, 0, time.Local)
	w.judged(t, curSHA, verify.StatusVerified, at)
	w.judged(t, accSHA, verify.StatusFailed, at)

	if err := w.run(t, "list"); err != nil {
		t.Fatal(err)
	}
	out := w.out.String()
	for sha, wants := range map[string][]string{
		curSHA: {"org/m", "current", "1.5 MB", "verified 2026-11-24 09:12"},
		accSHA: {"accepted", "does not match"},
		oldSHA: {"not live", "never verified"},
	} {
		l := line(out, sha)
		for _, want := range wants {
			if !strings.Contains(l, want) {
				t.Errorf("line for %s is %q; want %q in it\n%s", sha[:4], l, want, out)
			}
		}
	}
	if strings.Contains(out, "org/other") {
		t.Errorf("an alias with nothing cached is listed:\n%s", out)
	}
	if len(w.verified) != 0 {
		t.Errorf("list asked the server: %v", w.verified)
	}
}

func TestModelListWithNothingCachedSaysSo(t *testing.T) {
	w := newModelWorld(t)
	if err := w.run(t, "list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.out.String(), "flockadoo model download") {
		t.Errorf("empty list does not say how to download: %q", w.out.String())
	}
}

func TestModelVerifyShowsTheServersVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, sha string
		answer    verify.Status
		want      string
		fails     bool
	}{
		{"the current commit, verified", curSHA, verify.StatusVerified, "verified (current)", false},
		{"the accepted commit, verified", accSHA, verify.StatusVerified, "verified (accepted: the next approved revision retires it)", false},
		{"a changed file", curSHA, verify.StatusFailed, "does not match", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newModelWorld(t)
			w.weights(t, "org/m", tc.sha, 10)
			w.answers[tc.sha] = tc.answer
			err := w.run(t, "verify", "--revision", tc.sha)
			if (err != nil) != tc.fails {
				t.Errorf("err %v, want failure %v", err, tc.fails)
			}
			if !strings.Contains(w.out.String(), tc.want) {
				t.Errorf("output %q, want %q", w.out.String(), tc.want)
			}
			if !slices.Equal(w.verified, []string{"org/m@" + tc.sha}) {
				t.Errorf("asked %v, want org/m@%s", w.verified, tc.sha)
			}
		})
	}
}

// Without --revision, verify measures the commit the client serves.
func TestModelVerifyMeasuresTheConfiguredCommit(t *testing.T) {
	w := newModelWorld(t)
	w.weights(t, "org/m", curSHA, 10)
	w.weights(t, "org/m", accSHA, 10)
	w.cfg.Provider.HFRevision = accSHA
	w.answers[accSHA] = verify.StatusVerified
	if err := w.run(t, "verify"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.verified, []string{"org/m@" + accSHA}) {
		t.Errorf("asked %v, want the configured commit org/m@%s", w.verified, accSHA)
	}
}

func TestModelVerifyRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a commit not in the cache", []string{"verify", "--revision", curSHA}, "flockadoo model download"},
		{"nothing cached for the alias", []string{"verify"}, "flockadoo model download"},
		{"an alias outside the catalog", []string{"verify", "org/unknown"}, "org/unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newModelWorld(t)
			err := w.run(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v, want a refusal naming %q", err, tc.want)
			}
			if len(w.verified) != 0 {
				t.Errorf("asked the server: %v", w.verified)
			}
		})
	}
}
