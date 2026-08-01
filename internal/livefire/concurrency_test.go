//go:build livefire

package livefire

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// WP11 — H-26, hactl is never the only caller.
//
// Findings #61 #100 #101, all three of them the same premise: hactl treated
// the instance directory as private. It is shared — by a second terminal, a CI
// job, an MCP server, or the multi-agent fleet all three were reported from —
// and every one of these cases puts a second caller there deliberately.

// raceAutomation is the automation the concurrency cases rewrite, chosen per
// profile.
//
// On the live instance it is the object finding #100 was reported against,
// reused rather than recreated (FIXPLAN §5: the playground survives on
// purpose). On the rig it is a fixture entry, because a case that skipped the
// rig for want of a `pg_` name would leave the regression half of this tier
// with nothing — the rig is what keeps the claim true after the live
// reproduction is gone.
func raceAutomation(tgt Target) string {
	if tgt.Profile == Live {
		return "pg_f3_auto_1"
	}
	return "climate_schedule"
}

// candidateWith writes a candidate automation carrying a distinguishable
// description, and returns its path.
func candidateWith(t *testing.T, dir, id, marker string) string {
	t.Helper()
	body := "id: " + id + "\n" +
		"alias: " + id + "\n" +
		"description: " + marker + "\n" +
		"triggers:\n- hours: '0'\n  trigger: time_pattern\n" +
		"conditions: []\n" +
		"actions: []\n" +
		"mode: single\n"
	path := filepath.Join(dir, marker+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSweepConcurrentAppliesDoNotShareAStaleRead is finding #100.
//
// Two `auto apply --confirm` calls to one automation, launched together. Each
// fetches the stored entry (that fetch is also its backup) and then writes.
// Without exclusion both fetched the SAME starting state inside a window that
// measured 2.1 seconds on the reference instance, both printed their own diff,
// both printed `applied` and `reload: ok`, both exited 0 — and one of the two
// edits was not in the file afterwards.
//
// The assertion is not "both writes survive": two edits to one field cannot
// both survive, and the second overwriting the first is what two sequential
// applies do. It is that **no writer planned against a state another writer
// had already replaced** — which is exactly what the backups record. Before,
// both backups held the starting state; after, the second holds the first's
// result.
func TestSweepConcurrentAppliesDoNotShareAStaleRead(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		id := raceAutomation(tgt)
		if !automationExists(t, tgt, id) {
			t.Skipf("%s: R11 — `auto apply` writes through the companion and the rig boots HA "+
				"alone, so %s cannot be read here; the dash case below carries this clause on "+
				"both profiles over a companion-free write path", Finding(100), id)
		}
		work := t.TempDir()
		base := candidateWith(t, work, id, "pg_wp11_race_base")
		one := candidateWith(t, work, id, "pg_wp11_race_one")
		two := candidateWith(t, work, id, "pg_wp11_race_two")
		vocab := []string{"auto", "apply", id, "--file", "--confirm", base, one, two}

		// The case makes its own starting state, and this is not tidiness.
		// `auto apply` short-circuits on an empty diff — "no changes detected",
		// no write, NO BACKUP — and this case leaves the automation holding one
		// of its own two candidates. So the second run of it had one racer find
		// nothing to do, one backup where the assertion wants two, and a red
		// that said "a recovery point was overwritten" about a write that never
		// happened. WP10's lesson 7, in the other direction: a case whose
		// precondition is "the target is not already X" has to establish it,
		// because the acceptance run is never the first run.
		for _, file := range []string{base, one, two} {
			if plan, err := tgt.Read(t, "auto", "apply", id, "--file", file); err != nil {
				t.Fatalf("dry run with %s failed: %v\n%s", filepath.Base(file), err, plan)
			}
		}
		if out, err := tgt.Write(t, []string{id}, vocab,
			[]string{"auto", "apply", id, "--file", base, "--confirm"}); err != nil {
			t.Fatalf("establishing the pre-race state: %v\n%s", err, out)
		}
		if got := automationDescription(t, tgt, id); got != "pg_wp11_race_base" {
			t.Fatalf("pre-race state is %q, want pg_wp11_race_base — both racers must carry a real change", got)
		}

		before := backupContents(t, tgt, id)
		runs := tgt.WriteConcurrently(t, []string{id}, vocab,
			[]string{"auto", "apply", id, "--file", one, "--confirm"},
			[]string{"auto", "apply", id, "--file", two, "--confirm"},
		)
		for _, r := range runs {
			if r.Err != nil {
				t.Fatalf("%v exited %d: %v\n%s", r.Args, r.Exited(), r.Err, r.Stdout)
			}
		}

		// The two backups this race added, in the order they were written.
		fresh := newBackups(before, backupContents(t, tgt, id))
		if len(fresh) != 2 {
			t.Fatalf("%s: two confirmed writes produced %d backups, want 2 — a recovery point was "+
				"overwritten (see %s)", Finding(100), len(fresh), Finding(101))
		}
		final := automationDescription(t, tgt, id)
		if final != "pg_wp11_race_one" && final != "pg_wp11_race_two" {
			t.Fatalf("%s: neither writer's content is live; %s reads %q", Finding(100), id, final)
		}
		// The later backup must hold the earlier writer's result. If it holds
		// the pre-race state instead, both writers read before either wrote:
		// the losing edit was planned against a state that no longer existed
		// by the time it landed, and nothing told its caller.
		loser := "pg_wp11_race_one"
		if final == loser {
			loser = "pg_wp11_race_two"
		}
		if !strings.Contains(fresh[1].body, loser) {
			t.Errorf("%s: the second writer's backup does not hold the first writer's result.\n"+
				"  live now:            %s\n  second backup holds: %s\n"+
				"Both writers read the same starting state, so the write that lost was planned "+
				"against a state that had already been replaced — and `auto rollback` cannot "+
				"recover the version that was destroyed.",
				Finding(100), final, describedBy(fresh[1].body))
		}
	})
}

// TestSweepAConfirmedWriteNeverOverwritesABackup is finding #101.
//
// The live reproduction, kept: a file planted at the path the next backup
// would choose, and the write ran over it and reported success. It needs no
// concurrency at all, which is the sharper way to state the defect — the name
// carried one second of resolution and nothing asked whether it was free.
func TestSweepAConfirmedWriteNeverOverwritesABackup(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		id := raceAutomation(tgt)
		if !automationExists(t, tgt, id) {
			t.Skipf("%s: R11 — `auto apply` writes through the companion and the rig boots HA "+
				"alone, so %s cannot be read here; the dash case below carries this clause on "+
				"both profiles over a companion-free write path", Finding(101), id)
		}
		planted := plantOldFormatBackups(t, filepath.Join(tgt.Dir, "backups"), id)

		work := t.TempDir()
		file := candidateWith(t, work, id, "pg_wp11_sentinel")
		if plan, err := tgt.Read(t, "auto", "apply", id, "--file", file); err != nil {
			t.Fatalf("dry run failed: %v\n%s", err, plan)
		}
		out, err := tgt.Write(t, []string{id},
			[]string{"auto", "apply", id, "--file", "--confirm", file},
			[]string{"auto", "apply", id, "--file", file, "--confirm"})
		if err != nil {
			t.Fatalf("confirmed apply failed: %v\n%s", err, out)
		}

		for _, p := range planted {
			raw, readErr := os.ReadFile(filepath.Clean(p))
			if readErr != nil {
				t.Errorf("%s: %s is gone — the backup destroyed a file that was already there",
					Finding(101), filepath.Base(p))
				continue
			}
			if string(raw) != oldFormatSentinel {
				t.Errorf("%s: %s was overwritten by the confirmed write, which reported it as a "+
					"safe recovery point. A backup that destroys a backup is not a backup.",
					Finding(101), filepath.Base(p))
			}
		}
	})
}

// TestSweepAConfirmRequiresItsOwnDryRun is finding #61.
//
// The guard used to ask whether the family how-to had reached the session, out
// of state under the key `default` that every process sharing the instance
// directory writes. Measured live: one process running `area ls` under
// HACTL_MANUAL_MODE=full switched the guard off for a second process, in
// another directory, in progressive mode, whose first-ever automation command
// then wrote to the instance with nothing on stderr.
//
// A dry-run of the same target is evidence about the write rather than about
// the directory. This case proves both directions and the leak that used to
// exist: a READ of the family delivers the how-to and must not authorize
// anything.
func TestSweepAConfirmRequiresItsOwnDryRun(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		id := raceAutomation(tgt)
		if !automationExists(t, tgt, id) {
			t.Skipf("%s: R11 — `auto apply` writes through the companion and the rig boots HA "+
				"alone, so %s cannot be read here; the dash case below carries this clause on "+
				"both profiles over a companion-free write path", Finding(61), id)
		}
		// A session this instance has never seen, made by this process — the
		// precondition is "nothing has previewed this yet", and WP10 lesson 7
		// is that a case whose precondition is "this has never happened" must
		// make its own, because the acceptance run is never the first run.
		session := "pg_wp11_" + strings.ReplaceAll(t.Name(), "/", "_") + "_" + itoa(os.Getpid())
		t.Setenv("HACTL_SESSION", session)
		t.Setenv("HACTL_MANUAL_MODE", "progressive")

		work := t.TempDir()
		file := candidateWith(t, work, id, "pg_wp11_witness")
		vocab := []string{"auto", "apply", id, "--file", "--confirm", file}

		// A family READ first. It delivers the 'auto' how-to — which is
		// precisely what used to be enough to authorize the write below.
		if _, err := tgt.Read(t, "auto", "ls"); err != nil {
			t.Fatalf("auto ls failed: %v", err)
		}
		before := automationDescription(t, tgt, id)

		stderr, err := tgt.ReadDiagnostic(t, "auto", "apply", id, "--file", file, "--confirm")
		if err == nil {
			t.Fatalf("%s: `auto apply %s --confirm` was allowed with no dry-run behind it; a "+
				"listing is not a plan", Finding(61), id)
		}
		if !strings.Contains(stderr, "--confirm refused") {
			t.Fatalf("%s: the write failed for some other reason than the guard:\n%s", Finding(61), truncate(stderr))
		}
		if !strings.Contains(stderr, "without --confirm") {
			t.Errorf("%s: the refusal does not name the dry-run to run:\n%s", Finding(61), truncate(stderr))
		}
		if after := automationDescription(t, tgt, id); after != before {
			t.Fatalf("%s: the refused write changed the instance: %q -> %q", Finding(61), before, after)
		}

		// And the documented sequence goes through.
		if plan, planErr := tgt.Read(t, "auto", "apply", id, "--file", file); planErr != nil {
			t.Fatalf("dry run failed: %v\n%s", planErr, plan)
		}
		out, writeErr := tgt.Write(t, []string{id}, vocab,
			[]string{"auto", "apply", id, "--file", file, "--confirm"})
		if writeErr != nil {
			t.Fatalf("%s: the previewed write was refused: %v\n%s", Finding(61), writeErr, out)
		}
	})
}

// --- helpers ---------------------------------------------------------------

type backupFile struct {
	name string
	body string
}

// backupContents reads every backup belonging to id, oldest name first.
func backupContents(t *testing.T, tgt Target, id string) []backupFile {
	t.Helper()
	dir := filepath.Join(tgt.Dir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []backupFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_"+id+".yaml") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		if readErr != nil {
			continue
		}
		out = append(out, backupFile{name: name, body: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// newBackups returns the entries in after that were not in before.
func newBackups(before, after []backupFile) []backupFile {
	seen := make(map[string]bool, len(before))
	for _, b := range before {
		seen[b.name] = true
	}
	var fresh []backupFile
	for _, a := range after {
		if !seen[a.name] {
			fresh = append(fresh, a)
		}
	}
	return fresh
}

// describedBy pulls the description line out of a stored entry, for an error
// message that names what the backup actually holds.
func describedBy(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "description:"); ok {
			return strings.TrimSpace(after)
		}
	}
	return "(no description)"
}

func automationDescription(t *testing.T, tgt Target, id string) string {
	t.Helper()
	out, err := tgt.Read(t, "auto", "cat", id, "--tokensmax", "0")
	if err != nil {
		return ""
	}
	return describedBy(out)
}

func automationExists(t *testing.T, tgt Target, id string) bool {
	t.Helper()
	_, err := tgt.Read(t, "auto", "cat", id, "--tokensmax", "0")
	return err == nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestSweepConcurrentDashWritesEachKeepTheirOwnSnapshot carries H-26's "held"
// and "unique" clauses on BOTH profiles.
//
// It exists because the three cases above cannot run on the rig: `auto apply`
// writes through the companion and `hatest` boots Home Assistant alone (rig
// capability R11), so the family the findings were reported against is
// live-only. A defect reproduced live and kept nowhere is a defect fixed once —
// §1 of the FIXPLAN is about exactly that asymmetry — so the SHAPE moves to a
// write path the rig can drive.
//
// `dash replace` is that path: it reads a dashboard's stored config, snapshots
// it, and writes the modified document back, all over Home Assistant's own
// WebSocket API. Same read-modify-write, same snapshot-before-overwrite, no
// companion. Two of them at once must produce two snapshots, and the second
// must hold the first's result.
func TestSweepConcurrentDashWritesEachKeepTheirOwnSnapshot(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		urlPath := "pg-wp11-race"
		title := "PG WP11 Race"
		// Every file this case names is created up front so the guard vocabulary
		// is complete before the first write rather than grown beside it — an
		// argument the case forgot to declare is one guardLiveWrite refuses, and
		// a vocab assembled in pieces is how one gets forgotten.
		work := t.TempDir()
		cfg := filepath.Join(work, "dash.json")
		v1 := filepath.Join(work, "v1.json")
		v2 := filepath.Join(work, "v2.json")
		vocab := []string{"dash", "create", "delete", "save", "replace", "--url-path", "--title",
			"--file", "--confirm", "--json", title, "pg_wp11_v0", "pg_wp11_v1", "pg_wp11_v2",
			cfg, v1, v2}

		// Its own dashboard, made and destroyed by this case, so neither
		// profile has anything of anybody's at stake.
		if plan, err := tgt.Read(t, "dash", "create", "--url-path", urlPath, "--title", title); err != nil {
			t.Skipf("dash create is unavailable here: %v\n%s", err, truncate(plan))
		}
		if out, err := tgt.Write(t, []string{urlPath}, vocab,
			[]string{"dash", "create", "--url-path", urlPath, "--title", title, "--confirm"}); err != nil {
			t.Fatalf("creating %s: %v\n%s", urlPath, err, out)
		}
		t.Cleanup(func() {
			_, _ = tgt.Read(t, "dash", "delete", urlPath)
			_, _ = tgt.Write(t, []string{urlPath}, vocab, []string{"dash", "delete", urlPath, "--confirm"})
		})

		// A config carrying the value the racers rewrite.
		if err := os.WriteFile(cfg, []byte(
			`{"views":[{"title":"pg_wp11_v0","cards":[{"type":"markdown","content":"pg_wp11_v0"}]}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if plan, err := tgt.Read(t, "dash", "save", urlPath, "--file", cfg); err != nil {
			t.Fatalf("dry run of the save failed: %v\n%s", err, truncate(plan))
		}
		if out, err := tgt.Write(t, []string{urlPath}, vocab,
			[]string{"dash", "save", urlPath, "--file", cfg, "--confirm"}); err != nil {
			t.Fatalf("saving the starting config: %v\n%s", err, out)
		}

		// Two full saves, not two replaces: `dash replace` rewrites a value it
		// finds, so under working exclusion the second racer finds nothing left
		// to rewrite and correctly writes nothing — a no-op, not a lost update,
		// and a case that asserted on two snapshots there would be asserting
		// that the fix does NOT work. A save always writes the whole document.
		for path, marker := range map[string]string{v1: "pg_wp11_v1", v2: "pg_wp11_v2"} {
			body := `{"views":[{"title":"` + marker + `","cards":[{"type":"markdown","content":"` + marker + `"}]}]}`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if plan, err := tgt.Read(t, "dash", "save", urlPath, "--file", path); err != nil {
				t.Fatalf("dry run of the %s save failed: %v\n%s", marker, err, truncate(plan))
			}
		}

		before := dashSnapshots(t, tgt, urlPath)
		runs := tgt.WriteConcurrently(t, []string{urlPath}, vocab,
			[]string{"dash", "save", urlPath, "--file", v1, "--confirm"},
			[]string{"dash", "save", urlPath, "--file", v2, "--confirm"},
		)
		for _, r := range runs {
			if r.Err != nil {
				t.Fatalf("%v exited %d: %v\n%s", r.Args, r.Exited(), r.Err, truncate(r.Stdout))
			}
		}

		fresh := newBackups(before, dashSnapshots(t, tgt, urlPath))
		if len(fresh) != 2 {
			t.Fatalf("%s: two confirmed dashboard writes produced %d snapshots, want 2 — a "+
				"recovery point was overwritten by the write that claimed to be making one",
				Finding(101), len(fresh))
		}
		// The second snapshot must show the first writer's result. If both hold
		// the pre-race document, the two writers read before either wrote: the
		// edit that lost is unrecoverable, because nothing snapshotted it.
		if strings.Contains(fresh[1].body, "pg_wp11_v0") {
			t.Errorf("%s: the second writer snapshotted the PRE-RACE document, so both writers "+
				"read before either wrote. The losing edit was planned against a state that had "+
				"already been replaced, and no snapshot holds the version it destroyed.", Finding(100))
		}
	})
}

// dashSnapshots reads the dashboard snapshots for one url_path, oldest name
// first — the dash family's counterpart to backupContents.
func dashSnapshots(t *testing.T, tgt Target, urlPath string) []backupFile {
	t.Helper()
	dir := filepath.Join(tgt.Dir, "backups", "dashboards")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []backupFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, urlPath+".") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		if readErr != nil {
			continue
		}
		out = append(out, backupFile{name: name, body: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// oldFormatSentinel is the content plantOldFormatBackups writes, and the only
// thing the cleanup will delete: a file whose bytes changed is a file the
// backup wrote over, and removing it would destroy the evidence.
const oldFormatSentinel = "SENTINEL: another caller's only rollback point\n"

// plantOldFormatBackups puts a file at every name the ONE-SECOND scheme could
// choose over the next twelve seconds — comfortably past the round trip a
// confirmed write needs — and returns the paths.
//
// A name that is already taken is skipped rather than overwritten: a real
// backup living there is somebody's undo, which is the very thing this case
// is about.
func plantOldFormatBackups(t *testing.T, dir, id string) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	var planted []string
	now := time.Now()
	for i := range 12 {
		name := now.Add(time.Duration(i)*time.Second).Format("2006-01-02T15-04-05") + "_" + id + ".yaml"
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(oldFormatSentinel), 0o600); err != nil {
			t.Fatal(err)
		}
		planted = append(planted, path)
	}
	t.Cleanup(func() {
		for _, p := range planted {
			if raw, err := os.ReadFile(filepath.Clean(p)); err == nil && string(raw) == oldFormatSentinel {
				_ = os.Remove(p)
			}
		}
	})
	return planted
}
