package benchmark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

func TestPlanRejectsOutOfRangeRepetitions(t *testing.T) {
	p, err := BuildPlan(PlanOptions{FixtureIDs: []string{"fixture"}, Clients: []Client{Weaver, SABnzbd}, Transports: []Transport{Plaintext}, Targets: []ExecutionTarget{DockerLinux}, Repetitions: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Runs {
		p.Runs[i].Repetition += 100
	}
	if err = p.Validate(); err == nil {
		t.Fatal("out-of-range repetition accepted")
	}
}

func TestOutputRejectsFlattenedTreeAndUnknownRetainedFiles(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "extra archive", true: "flattened tree"}[nested], func(t *testing.T) {
			fixtureDir, out := t.TempDir(), t.TempDir()
			data := []byte("disc member bytes")
			file := filepath.Join(out, "member.bin")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			hash, err := hashFile(file)
			if err != nil {
				t.Fatal(err)
			}
			name := "member.bin"
			if nested {
				name = "BDMV/STREAM/member.bin"
			} else {
				if err := os.WriteFile(filepath.Join(out, "leftover.rar"), []byte("extra"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeVerificationManifest(t, fixtureDir, []fixture.FileDigest{{Path: name, Size: int64(len(data)), BLAKE3: hash}})
			if _, err = VerifyOutput(fixtureDir, out); err == nil {
				t.Fatal("invalid final output passed")
			}
		})
	}
}

func TestTimingBoundsAreNotForgivenAsZero(t *testing.T) {
	if ObservationUncertaintyAcceptable(-1, 1000000000) {
		t.Fatal("negative observation interval accepted")
	}
	if ObservationUncertaintyAcceptable(0, 0) {
		t.Fatal("zero duration accepted")
	}
}
