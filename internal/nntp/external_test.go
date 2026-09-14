package nntp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

const externalTestArticle = 700 << 10

// externalTestNZB writes an NZB shaped like a real Nyuu post: two archive
// volumes and a recovery file, each subject ending in its part count and size.
func externalTestNZB(t *testing.T, mutate func([]NZBFile)) string {
	t.Helper()
	sizes := []struct {
		name string
		size int64
	}{
		{"rar-files.part1.rar", 26214400},
		{"rar-files.part2.rar", 26214400},
		{"rar-files.part3.rar", 26214400},
		{"rar-files.part4.rar", 26214400},
		{"rar-files.part5.rar", 5748804},
		{"par-files.par2", 40000},
	}
	files := make([]NZBFile, 0, len(sizes))
	for index, entry := range sizes {
		parts := int(ExpectedSegmentCount(entry.size, externalTestArticle))
		file := NZBFile{
			Poster:  "poster <poster@example.invalid>",
			Subject: fmt.Sprintf(`[%d/%d] - "%s" yEnc (1/%d) %d`, index+1, len(sizes), entry.name, parts, entry.size),
			Groups:  []NZBGroup{"alt.binaries.test"},
		}
		for part := 1; part <= parts; part++ {
			file.Segments = append(file.Segments, NZBSegment{Bytes: 739000, Number: part, MessageID: fmt.Sprintf("%d.%d@example.invalid", index, part)})
		}
		files = append(files, file)
	}
	if mutate != nil {
		mutate(files)
	}
	contents, err := MarshalNZB(files)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "post.nzb")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportExternalNZBWritesAFixtureTheChainAccepts(t *testing.T) {
	root := t.TempDir()
	request := ExternalImport{
		NZBPath:         externalTestNZB(t, nil),
		FixturesRoot:    root,
		FixtureID:       "sab-test",
		Class:           fixture.HeadlineFixtureClass,
		ArticleRawBytes: externalTestArticle,
	}
	manifest, err := ImportExternalNZB(request)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.External == nil || len(manifest.ExpectedFiles) != 0 {
		t.Fatalf("manifest = %#v, want an external post expecting no files", manifest)
	}
	if len(manifest.ArchiveFiles) != 6 || manifest.ArchiveFiles[0].Path != "rar-files.part1.rar" || manifest.ArchiveFiles[4].Size != 5748804 {
		t.Fatalf("archive files = %#v, want the subjects' names and sizes in NZB order", manifest.ArchiveFiles)
	}
	if manifest.External.ArticleRawBytes != externalTestArticle || len(manifest.External.NZBSHA256) != 64 || strings.Join(manifest.External.Groups, ",") != "alt.binaries.test" {
		t.Fatalf("external details = %#v", manifest.External)
	}
	if _, err := os.Stat(filepath.Join(root, "sab-test", "sab-test.nzb")); err != nil {
		t.Fatalf("the NZB was not copied under the fixture id: %v", err)
	}
	if _, err := ImportExternalNZB(request); err == nil || !strings.Contains(err.Error(), "never overwritten") {
		t.Fatalf("second import error = %v, want a refusal to overwrite", err)
	}
}

func TestImportExternalNZBRefusesAPostItCannotDescribeExactly(t *testing.T) {
	cases := map[string]struct {
		mutate  func([]NZBFile)
		article int
		want    string
	}{
		"another article size": {article: 750 << 10, want: "import it at the article size"},
		"no size in subject": {
			mutate: func(files []NZBFile) { files[0].Subject = `"rar-files.part1.rar" yEnc` },
			want:   "no yEnc part count",
		},
		"missing segment": {
			mutate: func(files []NZBFile) { files[0].Segments = files[0].Segments[1:] },
			want:   "declares",
		},
		"file listed twice": {
			mutate: func(files []NZBFile) { files[5].Subject = files[4].Subject; files[5].Segments = files[4].Segments },
			want:   "twice",
		},
		"path in name": {
			mutate: func(files []NZBFile) {
				files[5].Subject = strings.Replace(files[5].Subject, "par-files.par2", "../par-files.par2", 1)
			},
			want: "plain file name",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			article := tc.article
			if article == 0 {
				article = externalTestArticle
			}
			root := t.TempDir()
			_, err := ImportExternalNZB(ExternalImport{
				NZBPath:         externalTestNZB(t, tc.mutate),
				FixturesRoot:    root,
				FixtureID:       "sab-test",
				Class:           fixture.HeadlineFixtureClass,
				ArticleRawBytes: article,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(root, "sab-test")); statErr == nil {
				t.Fatal("a refused import still created its fixture directory")
			}
		})
	}
}
