package benchmark

import (
	"fmt"
	"sort"
	"strings"
)

// ArticleProfile is the decoded size of one posted article, and it is a
// stratum rather than a knob. Article size sets how many round trips a
// download costs and how much per-article header, decode and bookkeeping work
// a client does for the same number of bytes, so two runs at different
// article sizes are measuring different things. It travels with the plan into
// every run artifact and into the summarizer's pairing key for the same
// reason the server link's RTT does: so a result at one size can never be
// pooled with a result at another.
//
// RawBytes is the decoded payload an article carries, before yEnc or uuencode
// expansion and before headers. It is the number the posting tool is given:
// Nyuu's -a, and the split size the uuencode poster uses.
type ArticleProfile struct {
	ID       string `json:"id"`
	RawBytes int    `json:"raw_bytes"`
}

const (
	// Article750K is the corpus default and what current posting tools
	// produce: 768000 bytes, the round 750 KiB every widely used poster
	// defaults to today.
	Article750K = "750k"
	// Article384K is the realistic lower bound. 393216 bytes is what a poster
	// configured for 3000 yEnc lines emits, which was the common default
	// before the article size crept up, and it is still what a backfill of
	// older posts hands a client. It is not a claim about how many posts on
	// Usenet are at this size — the corpus makes no such claim — only that it
	// is a size real posts are at, and the smallest that is still common.
	Article384K = "384k"
)

var articleProfiles = map[string]int{
	Article750K: 750 << 10,
	Article384K: 384 << 10,
}

// DefaultArticleProfile is what a plan that does not mention article size
// gets, and what every plan written before the stratum existed meant.
func DefaultArticleProfile() ArticleProfile {
	return ArticleProfile{ID: Article750K, RawBytes: articleProfiles[Article750K]}
}

// ArticleProfileIDs lists the declared sizes in ascending byte order, for
// help text and error messages.
func ArticleProfileIDs() []string {
	ids := make([]string, 0, len(articleProfiles))
	for id := range articleProfiles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return articleProfiles[ids[i]] < articleProfiles[ids[j]] })
	return ids
}

// ResolveArticleProfile turns a declared id into a complete profile. Sizes
// are named, not free-form: an arbitrary byte count would multiply the strata
// without adding a shape any real post has, and every one of them would need
// its own seeded corpus.
func ResolveArticleProfile(id string) (ArticleProfile, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return DefaultArticleProfile(), nil
	}
	raw, ok := articleProfiles[trimmed]
	if !ok {
		return ArticleProfile{}, fmt.Errorf("unsupported article size %q (expected one of %s)", id, strings.Join(ArticleProfileIDs(), ", "))
	}
	return ArticleProfile{ID: trimmed, RawBytes: raw}, nil
}

// ResolveArticleProfileBytes turns a raw byte count back into the profile
// that declares it. The seeder takes a byte count, so this is how a seeded
// corpus reports which stratum it belongs to.
func ResolveArticleProfileBytes(rawBytes int) (ArticleProfile, error) {
	for id, raw := range articleProfiles {
		if raw == rawBytes {
			return ArticleProfile{ID: id, RawBytes: raw}, nil
		}
	}
	return ArticleProfile{}, fmt.Errorf("article size %d bytes is not a declared stratum (expected one of %s)", rawBytes, strings.Join(ArticleProfileIDs(), ", "))
}

func (p ArticleProfile) Validate() error {
	resolved, err := ResolveArticleProfile(p.ID)
	if err != nil {
		return err
	}
	if p != resolved {
		return fmt.Errorf("article profile %q declares %d bytes, but %q is %d bytes", p.ID, p.RawBytes, resolved.ID, resolved.RawBytes)
	}
	return nil
}

func (p ArticleProfile) String() string {
	return fmt.Sprintf("%s (%d bytes)", p.ID, p.RawBytes)
}
