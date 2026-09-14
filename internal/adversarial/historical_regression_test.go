package adversarial

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"testing"
)

func TestPublishedSABnzbdPAR2RegressionsEncodeExactPrimitives(t *testing.T) {
	direct, err := Generate("historical-regression-sab-cve-2021-29488-par2-parent-escape")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(direct.Data["payload-001.bin"], []byte("../escape.canary")) {
		t.Fatal("direct PAR2 regression is missing the parent escape")
	}

	for _, test := range []struct {
		id      string
		target  string
		symlink bool
	}{
		{"historical-regression-sab-ghsa-75g3-unpacked-par2-parent-escape", "../escape.canary", false},
		{"historical-regression-sab-ghsa-mjwj-symlink-dotdot-par2-escape", "pivot/../escape.canary", true},
	} {
		t.Run(test.id, func(t *testing.T) {
			bundle, err := Generate(test.id)
			if err != nil {
				t.Fatal(err)
			}
			archive := bundle.Data["payload-000.bin"]
			reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
			if err != nil {
				t.Fatal(err)
			}
			var sawIndex, sawSymlink bool
			for _, file := range reader.File {
				if file.Name == "pivot" && file.Mode()&os.ModeSymlink != 0 {
					sawSymlink = true
				}
				if file.Name != "fixture.par2" {
					continue
				}
				contents, err := readZipMember(file)
				if err != nil {
					t.Fatal(err)
				}
				sawIndex = bytes.Contains(contents, []byte(test.target))
			}
			if !sawIndex || sawSymlink != test.symlink {
				t.Fatalf("encoded primitive mismatch: index=%v symlink=%v", sawIndex, sawSymlink)
			}
		})
	}
}

func TestPublishedSABnzbdRegressionContractsDeclareReachability(t *testing.T) {
	for _, c := range Catalog() {
		if c.Family != "historical-regression" {
			continue
		}
		contract := contractFor(c)
		if contract.CoverageLimit == "recipe intent alone does not prove the target component was exercised" {
			t.Fatalf("%s has only the generic reachability limit", c.ID)
		}
		if len(contract.Prerequisites) < 4 {
			t.Fatalf("%s is missing its exploit-sink prerequisite", c.ID)
		}
	}
}

func readZipMember(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
