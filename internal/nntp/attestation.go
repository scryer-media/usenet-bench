package nntp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

// ArticleAttestation records the exact poster input contract alongside the
// resulting NZB. Counts alone cannot identify article size. This is generation
// provenance, not a claim that every body was independently decoded on readback.
type ArticleAttestation struct {
	SchemaVersion  int               `json:"schema_version"`
	RawBytes       int               `json:"raw_bytes"`
	PayloadBytes   int               `json:"payload_bytes"`
	Producer       string            `json:"producer"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	NZBSHA256      string            `json:"nzb_sha256"`
	Segments       []ArticleBoundary `json:"segments"`
}
type ArticleBoundary struct {
	File      string `json:"file"`
	Number    int    `json:"number"`
	MessageID string `json:"message_id"`
	Offset    int64  `json:"offset"`
	RawBytes  int64  `json:"raw_bytes"`
}

func digestJSON(v any) string       { raw, _ := json.Marshal(v); return digestBytes(raw) }
func digestBytes(raw []byte) string { d := sha256.Sum256(raw); return hex.EncodeToString(d[:]) }

func articleAttestation(nzbPath string, m fixture.GeneratedManifest, rawBytes int, producer string) (ArticleAttestation, error) {
	raw, err := os.ReadFile(nzbPath)
	if err != nil {
		return ArticleAttestation{}, err
	}
	payloadBytes, err := ArticlePayloadBytes(m.Case.PostEncodingOrDefault(), rawBytes)
	if err != nil {
		return ArticleAttestation{}, err
	}
	doc, err := UnmarshalNZB(raw)
	if err != nil {
		return ArticleAttestation{}, err
	}
	a := ArticleAttestation{SchemaVersion: 1, RawBytes: rawBytes, PayloadBytes: payloadBytes, Producer: producer, ManifestSHA256: digestJSON(m), NZBSHA256: digestBytes(raw)}
	for _, file := range doc.Files {
		name, err := nzbFileName(file.Subject)
		if err != nil {
			return a, err
		}
		var size int64 = -1
		for _, posted := range m.PostedFiles() {
			if posted.Path == name || fileBase(posted.Path) == name {
				size = posted.Size
				break
			}
		}
		if size < 0 {
			return a, fmt.Errorf("unexpected posted file %s", name)
		}
		for _, segment := range file.Segments {
			offset := int64(segment.Number-1) * int64(payloadBytes)
			if segment.Number < 1 || offset >= size || segment.MessageID == "" {
				return a, fmt.Errorf("invalid article boundary")
			}
			a.Segments = append(a.Segments, ArticleBoundary{name, segment.Number, segment.MessageID, offset, min(int64(payloadBytes), size-offset)})
		}
	}
	return a, nil
}

func WriteArticleAttestation(nzbPath string, m fixture.GeneratedManifest, rawBytes int, producer string) error {
	if producer == "" {
		return fmt.Errorf("poster identity required")
	}
	a, err := articleAttestation(nzbPath, m, rawBytes, producer)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(nzbPath+".articles.json", append(raw, '\n'), 0600)
}

func AssertNZBArticleSize(nzbPath string, m fixture.GeneratedManifest, rawBytes int) error {
	if err := AssertNZBSegmentCount(nzbPath, m, rawBytes); err != nil {
		return err
	}
	got, err := ReadArticleAttestation(nzbPath)
	if err != nil {
		return err
	}
	want, err := articleAttestation(nzbPath, m, rawBytes, got.Producer)
	if err != nil {
		return err
	}
	if got.Producer == "" || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("article attestation differs from exact requested size or seeded corpus; reseed corpus")
	}
	return got.ValidateFor(m, rawBytes, want.NZBSHA256)
}

func ReadArticleAttestation(nzbPath string) (ArticleAttestation, error) {
	var a ArticleAttestation
	f, err := os.Open(nzbPath + ".articles.json")
	if err != nil {
		return a, fmt.Errorf("article-size provenance missing; reseed corpus: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return a, err
	}
	if len(raw) > 16<<20 {
		return a, fmt.Errorf("article attestation exceeds 16 MiB")
	}
	err = json.Unmarshal(raw, &a)
	return a, err
}

// ValidateFor rechecks saved evidence without reopening the original corpus.
func (a ArticleAttestation) ValidateFor(m fixture.GeneratedManifest, rawBytes int, nzbDigest string) error {
	payload, err := ArticlePayloadBytes(m.Case.PostEncodingOrDefault(), rawBytes)
	if err != nil {
		return err
	}
	if a.SchemaVersion != 1 || a.RawBytes != rawBytes || a.PayloadBytes != payload || a.ManifestSHA256 != digestJSON(m) || a.NZBSHA256 != nzbDigest || strings.TrimSpace(a.Producer) == "" {
		return fmt.Errorf("article attestation identity differs from workload")
	}
	files := map[string]int64{}
	expected := int64(0)
	for _, f := range m.PostedFiles() {
		name := fileBase(f.Path)
		if _, exists := files[name]; exists || f.Size <= 0 {
			return fmt.Errorf("ambiguous or empty posted file")
		}
		files[name] = f.Size
		expected += ExpectedSegmentCount(f.Size, payload)
		if expected > 1000000 {
			return fmt.Errorf("article inventory exceeds limit")
		}
	}
	if int64(len(a.Segments)) != expected {
		return fmt.Errorf("article inventory does not cover all posted and withheld files")
	}
	seen := map[string]bool{}
	for _, s := range a.Segments {
		size, ok := files[s.File]
		if !ok || s.Number < 1 || int64(s.Number) > ExpectedSegmentCount(size, payload) || strings.TrimSpace(s.MessageID) == "" {
			return fmt.Errorf("invalid segment identity")
		}
		offset := int64(s.Number-1) * int64(payload)
		key := fmt.Sprintf("%s/%d", s.File, s.Number)
		if seen[key] || s.Offset != offset || s.RawBytes != min(int64(payload), size-offset) {
			return fmt.Errorf("duplicate or incorrect article boundary")
		}
		seen[key] = true
	}
	return nil
}
