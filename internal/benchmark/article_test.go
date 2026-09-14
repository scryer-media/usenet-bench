package benchmark

import (
	"strings"
	"testing"
)

func TestArticleProfilesAreOrderedAndResolvable(t *testing.T) {
	ids := ArticleProfileIDs()
	if len(ids) != 3 || ids[0] != Article384K || ids[1] != Article700K || ids[2] != Article750K {
		t.Fatalf("ArticleProfileIDs() = %v, want the strata in ascending byte order", ids)
	}
	for _, id := range ids {
		profile, err := ResolveArticleProfile(id)
		if err != nil {
			t.Fatalf("ResolveArticleProfile(%q) error = %v", id, err)
		}
		if err := profile.Validate(); err != nil {
			t.Fatalf("resolved profile %q does not validate: %v", id, err)
		}
		byBytes, err := ResolveArticleProfileBytes(profile.RawBytes)
		if err != nil || byBytes != profile {
			t.Fatalf("ResolveArticleProfileBytes(%d) = %#v, %v, want %#v", profile.RawBytes, byBytes, err, profile)
		}
	}
}

func TestDefaultArticleProfileIsTheLargeStratum(t *testing.T) {
	// Every corpus seeded before the stratum existed was posted at 750 KiB,
	// so the default has to stay there or old artifacts change stratum
	// without anything being reposted.
	profile := DefaultArticleProfile()
	if profile.ID != Article750K || profile.RawBytes != 768000 {
		t.Fatalf("DefaultArticleProfile() = %#v, want the 750 KiB stratum", profile)
	}
	resolved, err := ResolveArticleProfile("")
	if err != nil || resolved != profile {
		t.Fatalf("ResolveArticleProfile(\"\") = %#v, %v, want the default", resolved, err)
	}
}

func TestArticleProfileRefusesAnUndeclaredSize(t *testing.T) {
	// A size that is not a stratum would produce results nothing else in the
	// corpus can be paired with, so it is refused by name rather than
	// silently accepted as a third population.
	if _, err := ResolveArticleProfile("512k"); err == nil || !strings.Contains(err.Error(), "384k") {
		t.Fatalf("ResolveArticleProfile(\"512k\") error = %v, want a refusal naming the declared strata", err)
	}
	if _, err := ResolveArticleProfileBytes(500_000); err == nil {
		t.Fatal("an undeclared byte count should not resolve to a stratum")
	}
	inconsistent := ArticleProfile{ID: Article384K, RawBytes: 768000}
	if err := inconsistent.Validate(); err == nil {
		t.Fatal("a profile whose id and byte count disagree should not validate")
	}
}

func TestPlanRefusesRunsFromAnotherArticleStratum(t *testing.T) {
	plan, err := BuildPlan(PlanOptions{
		FixtureIDs:     []string{"fixture"},
		Clients:        []Client{Weaver},
		Transports:     []Transport{Plaintext},
		Targets:        []ExecutionTarget{DockerLinux},
		Repetitions:    1,
		ArticleProfile: ArticleProfile{ID: Article384K, RawBytes: 384 << 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ArticleProfile.ID != Article384K {
		t.Fatalf("plan article profile = %#v, want the 384 KiB stratum", plan.ArticleProfile)
	}
	for _, run := range plan.Runs {
		if run.ArticleProfile != plan.ArticleProfile {
			t.Fatalf("run %s carries %#v, want the plan's stratum", run.ID, run.ArticleProfile)
		}
	}
	plan.Runs[0].ArticleProfile = DefaultArticleProfile()
	if err := plan.Validate(); err == nil {
		t.Fatal("a run from another article stratum should not validate against its plan")
	}
}
