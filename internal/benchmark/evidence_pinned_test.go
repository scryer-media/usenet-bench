package benchmark

import (
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

const (
	pinnedPayloadDigest = "e3e9cc801c72d8c6eb294b9ebd3faa6a13d8acab40bd4aa4b8cf69a6a8d99f5d"
	otherPayloadDigest  = "bc297ac4b80f7e12430672b4d98ea63650accffc81c178550b8fe38aacdf8c1a"
)

func externalManifest() fixture.GeneratedManifest {
	return fixture.GeneratedManifest{
		SchemaVersion: 3,
		Case:          fixture.ArchiveCase{ID: "external"},
		ArchiveFiles:  []fixture.FileDigest{{Path: "post.part01.rar", Size: 1}},
		External:      &fixture.ExternalPostDetails{ArticleRawBytes: 700 << 10},
	}
}

func pinnedVerification(reference, actualPath, digest string) OutputVerification {
	return OutputVerification{
		FixtureID: "external",
		Files: []VerifiedOutputFile{{
			ExpectedPath: "job/payload.bin",
			ActualPath:   actualPath,
			Size:         2 << 20,
			BLAKE3:       digest,
		}},
		Reference: reference,
	}
}

// An external post's manifest expects no files; the verification's pinned
// reference is the oracle. A client that named the payload after its own job
// was matched by content when it ran, and its evidence must stay admissible.
func TestExternalVerificationIsAdmissibleAgainstItsPin(t *testing.T) {
	m := externalManifest()
	for _, v := range []OutputVerification{
		pinnedVerification(ReferencePinnedHere, "job/payload.bin", pinnedPayloadDigest),
		pinnedVerification(ReferencePinned, "other-job/renamed.bin", pinnedPayloadDigest),
	} {
		if err := v.ValidateFor(m); err != nil {
			t.Fatalf("verification %+v refused: %v", v, err)
		}
	}
}

func TestExternalVerificationWithoutAPinIsRefused(t *testing.T) {
	m := externalManifest()
	cases := map[string]OutputVerification{
		"no reference": pinnedVerification("", "job/payload.bin", pinnedPayloadDigest),
		"no files":     {FixtureID: "external", Reference: ReferencePinned},
	}
	small := pinnedVerification(ReferencePinned, "job/payload.bin", pinnedPayloadDigest)
	small.Files[0].Size = 19
	cases["under the pin floor"] = small
	for name, v := range cases {
		if err := v.ValidateFor(m); err == nil {
			t.Fatalf("%s: verification %+v admitted", name, v)
		}
	}
}

func TestGeneratedVerificationNamingAPinIsRefused(t *testing.T) {
	m := externalManifest()
	m.External = nil
	m.ExpectedFiles = []fixture.FileDigest{{Path: "job/payload.bin", Size: 2 << 20, BLAKE3: pinnedPayloadDigest}}
	if err := pinnedVerification("", "job/payload.bin", pinnedPayloadDigest).ValidateFor(m); err != nil {
		t.Fatalf("generated verification refused: %v", err)
	}
	if err := pinnedVerification(ReferencePinned, "job/payload.bin", pinnedPayloadDigest).ValidateFor(m); err == nil {
		t.Fatal("generated fixture verification naming a pin was admitted")
	}
}

func externalArtifact(suite string, v OutputVerification) QueueArtifact {
	return QueueArtifact{SuiteID: suite, Jobs: []QueueJobArtifact{{
		Run:          Run{FixtureID: "external"},
		Workload:     &WorkloadEvidence{Manifest: externalManifest()},
		Verification: &v,
	}}}
}

// The pin lives beside the fixture, not in the artifacts: runs verified
// against different pins compared clients on different payloads.
func TestPinnedAgreementRefusesRunsVerifiedAgainstDifferentPins(t *testing.T) {
	agreeing := []QueueArtifact{
		externalArtifact("sequential-0001", pinnedVerification(ReferencePinnedHere, "job/payload.bin", pinnedPayloadDigest)),
		externalArtifact("sequential-0002", pinnedVerification(ReferencePinned, "other/renamed.bin", pinnedPayloadDigest)),
	}
	if err := ValidatePinnedAgreement(agreeing); err != nil {
		t.Fatalf("agreeing runs refused: %v", err)
	}
	disagreeing := append(agreeing, externalArtifact("sequential-0003", pinnedVerification(ReferencePinned, "job/payload.bin", otherPayloadDigest)))
	err := ValidatePinnedAgreement(disagreeing)
	if err == nil || !strings.Contains(err.Error(), "different pinned output") {
		t.Fatalf("disagreeing runs: %v", err)
	}
}
