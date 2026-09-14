package fixture

import (
	"encoding/json"
	"fmt"
	"os"
)

// GeneratedManifestSchemaVersion is the schema every newly generated fixture
// manifest is written at. Older manifests stay readable; the loader fills in
// the fields their schema predates. Schema 9 adds withheld-article faults,
// which a seeder that predates them would post in full, so it refuses them.
const GeneratedManifestSchemaVersion = 9

// FileDigest describes a fixture input, archive volume, or repair artifact.
// Paths are always relative to the fixture directory.
type FileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	BLAKE3 string `json:"blake3"`
}

// GeneratedManifest is written beside every generated fixture. It is the
// oracle for a client run: clients must produce all ExpectedFiles exactly,
// independent of how they download or unpack the archive.
type GeneratedManifest struct {
	SchemaVersion int         `json:"schema_version"`
	Case          ArchiveCase `json:"case"`
	Toolchain     ToolchainID `json:"toolchain"`
	// ArchiveWriterToolchain identifies the pinned writer that produced the
	// container. For the RAR lanes it repeats Toolchain; for the 7z lane it is
	// the official 7-Zip build, while Toolchain remains the image that
	// rendered the payload.
	ArchiveWriterToolchain ToolchainID   `json:"archive_writer_toolchain"`
	PayloadRecipe          PayloadRecipe `json:"payload_recipe"`
	ExpectedFiles          []FileDigest  `json:"expected_files"`
	// SourceArchiveFiles records the intact archive volumes before deliberate
	// corruption. It makes the repair target independently auditable.
	SourceArchiveFiles []FileDigest `json:"source_archive_files,omitempty"`
	// ArchiveFiles is the exact set of files posted through NNTP: damaged
	// archive volumes plus all PAR2 or RAR recovery material.
	ArchiveFiles []FileDigest `json:"archive_files"`
	// WithheldFiles are listed in the NZB but never posted. Their bytes stay
	// on disk so the repair target remains auditable; the seeder emits NZB
	// entries whose article identifiers were never sent to the server, which
	// is what a real short post looks like to a client.
	WithheldFiles []FileDigest `json:"withheld_files,omitempty"`
	// NZBFileOrder is the exact posting order for this fixture, and
	// NZBOrderSeed the value the permutation was drawn from. Both are
	// functions of the fixture id, so a reseed reproduces them.
	NZBFileOrder []string      `json:"nzb_file_order"`
	NZBOrderSeed uint64        `json:"nzb_order_seed"`
	Repair       RepairDetails `json:"repair"`
	// Compression records what the writer actually achieved: the payload
	// bytes handed to it, the container bytes it produced, and the ratio
	// between them. A compression lane that turns out not to compress is a
	// lane measuring the wrong thing, and this is where that shows.
	Compression CompressionDetails `json:"compression"`
	// Sidecars are non-archive files posted alongside the container, such as
	// an SFV checksum list. They are posted material, so they are already in
	// ArchiveFiles; this names them and says what wrote them.
	Sidecars []SidecarDetails `json:"sidecars,omitempty"`
	// InnerArchive describes a second container between the payload and the
	// posted one, for the lanes that wrap.
	InnerArchive *InnerArchiveDetails `json:"inner_archive,omitempty"`
	// ZipStructure is what the posted zip was found to contain when it was
	// parsed back off disk. Every zip lane carries it, including the ordinary
	// ones, so "this archive is zip64" is a reading of the bytes rather than a
	// restatement of the switch the writer was given.
	ZipStructure *ZipStructureDetails `json:"zip_structure,omitempty"`
	// Encoding is how the fixture's article bodies are encoded. It repeats
	// Case.Encoding so a reader of the manifest need not know which field is
	// authoritative; the two are written together and checked on load.
	Encoding PostEncoding `json:"encoding"`
	// External is present on a fixture the benchmark did not make: a post
	// already on a real provider, imported from its NZB. Nothing about its
	// payload is known in advance, so its ExpectedFiles may be empty; the
	// oracle is then the output pinned beside it (see PinnedOutput).
	External *ExternalPostDetails `json:"external,omitempty"`
}

// ExternalPostDetails records where an imported post came from. The NZB's
// digest is what ties every result to one post, since the article bodies live
// on a server the benchmark cannot inspect.
type ExternalPostDetails struct {
	Description string `json:"description,omitempty"`
	NZBSHA256   string `json:"nzb_sha256"`
	// ArticleRawBytes is the article size the poster split the files at,
	// read off the NZB's own segment counts.
	ArticleRawBytes int      `json:"article_raw_bytes"`
	Groups          []string `json:"groups"`
}

// PinnedOutputName is the file beside an external fixture's manifest that
// holds its pinned output.
const PinnedOutputName = "expected-output.json"

// PinnedOutput is the oracle for an external fixture whose payload was not
// known when it was imported. The first run that finishes pins what it
// extracted, and every later run, by any client, must reproduce those files
// byte for byte. A wrong pin cannot pass quietly: it fails every other
// client's runs.
type PinnedOutput struct {
	SchemaVersion int          `json:"schema_version"`
	FixtureID     string       `json:"fixture_id"`
	PinnedAt      string       `json:"pinned_at"`
	PinnedFrom    string       `json:"pinned_from"`
	Files         []FileDigest `json:"files"`
}

// CompressionDetails is the writer's measured result for this fixture.
type CompressionDetails struct {
	// Method is the matrix's declared compression for this case, repeated
	// here so the numbers below are never read without it.
	Method Compression `json:"method"`
	// PayloadBytes is the total size of the files handed to the writer, and
	// ArchiveBytes the total size of the container it produced. For the
	// wrapped lanes PayloadBytes is still the media, not the inner archive.
	PayloadBytes int64 `json:"payload_bytes"`
	ArchiveBytes int64 `json:"archive_bytes"`
	// Ratio is ArchiveBytes/PayloadBytes: 1.0 for a stored lane, below 1.0
	// where the codec did something.
	Ratio float64 `json:"ratio"`
}

// SidecarDetails names one posted non-archive file and the tool that
// validated it.
type SidecarDetails struct {
	Kind Sidecar `json:"kind"`
	Path string  `json:"path"`
	// VerifiedBy is the pinned reader that confirmed the sidecar's contents
	// against the files it describes, at generation time.
	VerifiedBy ToolchainID `json:"verified_by"`
}

// InnerArchiveDetails describes the container the outer writer stored.
type InnerArchiveDetails struct {
	Kind InnerArchive `json:"kind"`
	Path string       `json:"path"`
	Size int64        `json:"size"`
	// Toolchain is the pinned writer that produced the inner container, which
	// is not in general the one that produced the outer.
	Toolchain ToolchainID `json:"toolchain"`
}

// ZipStructureDetails is what a posted zip actually carries, read back out of
// the archive the writer produced. Nothing in it comes from the switch the
// matrix asked for: Zip64 is true because the parser found zip64 records, and
// DataDescriptorEntries counts local headers that really do set general
// purpose bit 3. A lane whose declared structure is not what the bytes carry
// is refused at generation time rather than posted under a name it has not
// earned.
type ZipStructureDetails struct {
	// Declared is the matrix's zip_structure for this case, repeated here so
	// the findings below are never read without the claim they were checked
	// against. Empty is the ordinary zip the other zip lanes carry.
	Declared ZipStructure `json:"declared,omitempty"`
	// InspectedFile is the archive file that was parsed: the one a reader is
	// handed to open the set.
	InspectedFile string `json:"inspected_file"`
	// Zip64 is the headline finding: the archive carries zip64 structures,
	// either an end-of-central-directory record and its locator or a zip64
	// extra field on an entry.
	Zip64                  bool `json:"zip64"`
	Zip64EOCDRecord        bool `json:"zip64_eocd_record"`
	Zip64EOCDLocator       bool `json:"zip64_eocd_locator"`
	Zip64ExtraFieldEntries int  `json:"zip64_extra_field_entries"`
	// Entries is how many members the central directory lists, and
	// LocalHeadersInspected how many of them live in the inspected file — a
	// spanned set keeps most of its local headers on earlier parts.
	Entries               int `json:"entries"`
	LocalHeadersInspected int `json:"local_headers_inspected"`
	// DataDescriptorEntries counts inspected local headers with general
	// purpose bit 3 set, so the member's CRC and sizes trail the data instead
	// of preceding it.
	DataDescriptorEntries int `json:"data_descriptor_entries"`
	// LargestMemberBytes is the biggest uncompressed member size the central
	// directory records. Past 4 GiB it can only be expressed in a zip64 extra
	// field.
	LargestMemberBytes int64 `json:"largest_member_bytes"`
}

// RepairDetails describes the bounded fault injected into a fixture. The
// expected extracted files remain the output oracle; this metadata makes the
// input fault and repair strength reproducible without retaining duplicate
// intact archive bytes.
type RepairDetails struct {
	Profile               RepairProfile `json:"profile"`
	PAR2RedundancyPercent int           `json:"par2_redundancy_percent,omitempty"`
	RARRecoveryVolumes    int           `json:"rar_recovery_volumes,omitempty"`
	// PAR3 records how a PAR3 profile's recovery material was written, so a
	// timing change between releases can be told apart from a corpus change.
	PAR3        *PAR3Details       `json:"par3,omitempty"`
	Corruptions []CorruptionDetail `json:"corruptions,omitempty"`
}

// PAR3Details names the reference build and the exact creation parameters of
// a PAR3 repair fixture.
type PAR3Details struct {
	Toolchain         ToolchainID `json:"toolchain"`
	Code              string      `json:"code"`
	RedundancyPercent int         `json:"redundancy_percent"`
	// BlockSize is zero for embedded PAR3, where the reference chooses it.
	BlockSize int64    `json:"block_size,omitempty"`
	Embedded  bool     `json:"embedded,omitempty"`
	Arguments []string `json:"arguments"`
	// Cohorts is the number of interleaved FFT cohorts the creation asked
	// for; zero leaves the choice to the reference.
	Cohorts int `json:"cohorts,omitempty"`
	// PAR2AtLimit is set for the lane past PAR2's block limit.
	PAR2AtLimit *PAR2LimitComparison `json:"par2_at_limit,omitempty"`
}

// PAR2LimitComparison is what the same withheld articles would cost a PAR2
// set over the same post. PAR2 can repair only when it holds at least as many
// recovery blocks as there are damaged source blocks, so the comparison is
// exact without writing the set.
type PAR2LimitComparison struct {
	// BlockSize is PAR2's smallest legal block size for the post: the
	// smallest multiple of four that keeps it within PAR2BlockLimit blocks.
	BlockSize    int64 `json:"block_size"`
	SourceBlocks int64 `json:"source_blocks"`
	// DamagedBlocks counts the distinct PAR2 blocks the withheld articles
	// touch; one article usually straddles two.
	DamagedBlocks int64 `json:"damaged_blocks"`
	// RecoveryBlocks is what the same redundancy percentage buys PAR2.
	RecoveryBlocks int64 `json:"recovery_blocks"`
	// RequiredRedundancyPercent is the least whole percentage that repairs.
	RequiredRedundancyPercent int `json:"required_redundancy_percent"`
	// PAR3LostBlocks is the PAR3 lane's loss for the same articles: one
	// block per article.
	PAR3LostBlocks   int64 `json:"par3_lost_blocks"`
	PAR3SourceBlocks int64 `json:"par3_source_blocks"`
}

// WithheldArticleFault is the CorruptionDetail kind of one article the server
// refuses.
const WithheldArticleFault = "withheld-article"

// CorruptionDetail records a deterministic mutation or intentional omission.
// Offset and Length are populated for byte-flip and withheld-article faults.
// Kind is one of byte-flip, missing-volume (absent from the NZB entirely),
// withheld-volume (listed in the NZB, never posted) or withheld-article (one
// article of a posted file whose NZB segment names an identifier that was
// never posted; Offset and Length are the raw bytes that article carries).
type CorruptionDetail struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Length int    `json:"length,omitempty"`
}

// PayloadRecipe records every size and count used by the generator. The
// matrix describes the kind of workload; this recipe records the exact scale
// used for this specific generated fixture.
type PayloadRecipe struct {
	Layout              PayloadLayout `json:"layout"`
	UniformBytesPerFile int64         `json:"uniform_bytes_per_file,omitempty"`
	// SampleNoiseBits is the width of the deterministic per-sample noise added
	// to a compressible video payload after rendering; zero for every other
	// payload. It is what sets a compressible archive's size, so it is part
	// of the recipe.
	SampleNoiseBits uint  `json:"sample_noise_bits,omitempty"`
	LargeFileBytes  int64 `json:"large_file_bytes,omitempty"`
	MediumFileCount int   `json:"medium_file_count,omitempty"`
	MediumFileBytes int64 `json:"medium_file_bytes,omitempty"`
	SmallFileCount  int   `json:"small_file_count,omitempty"`
	SmallFileBytes  int64 `json:"small_file_bytes,omitempty"`
}

// ToolchainID is stored separately so reports identify both the requested
// archive compatibility family and the exact RARLAB generator release.
type ToolchainID struct {
	ID       string `json:"id"`
	Image    string `json:"image"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Platform string `json:"platform"`
	Binary   string `json:"binary,omitempty"`
	// Version is the upstream release the toolchain installs, where the
	// toolchain declares one separately from its id.
	Version string `json:"version,omitempty"`
	// Packages records the exact distribution package versions an image
	// installs, for the writers that come from a distribution rather than an
	// upstream tarball. The image is digest-pinned and the generator refuses
	// to run when the versions inside it differ from the ones the toolchain
	// file declares, so this is a recorded fact, not a hope.
	Packages map[string]string `json:"packages,omitempty"`
}

func LoadGeneratedManifest(path string) (GeneratedManifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return GeneratedManifest{}, fmt.Errorf("read fixture manifest %s: %w", path, err)
	}
	var manifest GeneratedManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return GeneratedManifest{}, fmt.Errorf("decode fixture manifest %s: %w", path, err)
	}
	if manifest.SchemaVersion < 1 || manifest.SchemaVersion > GeneratedManifestSchemaVersion {
		return GeneratedManifest{}, fmt.Errorf("unsupported generated fixture schema version %d", manifest.SchemaVersion)
	}
	if manifest.Case.ID == "" || (len(manifest.ExpectedFiles) == 0 && manifest.External == nil) || len(manifest.ArchiveFiles) == 0 {
		return GeneratedManifest{}, fmt.Errorf("fixture manifest %s is incomplete", path)
	}
	if manifest.SchemaVersion < 4 {
		manifest.Case.RepairProfile = CleanRepairProfile
		manifest.Repair = RepairDetails{Profile: CleanRepairProfile}
		manifest.SourceArchiveFiles = append([]FileDigest(nil), manifest.ArchiveFiles...)
	} else if !manifest.Case.RepairProfile.Valid() || manifest.Case.RepairProfile != manifest.Repair.Profile || len(manifest.SourceArchiveFiles) == 0 {
		return GeneratedManifest{}, fmt.Errorf("fixture manifest %s has invalid repair metadata", path)
	}
	if manifest.SchemaVersion < 6 {
		// Schema 5 and earlier predate the posting-order axis. Every one of
		// those fixtures was posted in sorted archive order.
		manifest.Case.NZBOrder = SequentialNZBOrder
		manifest.NZBFileOrder = postedPaths(manifest.ArchiveFiles)
		manifest.NZBOrderSeed = 0
		return manifest, nil
	}
	if !manifest.Case.NZBOrder.Valid() {
		return GeneratedManifest{}, fmt.Errorf("fixture manifest %s has unsupported nzb_order %q", path, manifest.Case.NZBOrder)
	}
	if err := validateNZBFileOrder(manifest); err != nil {
		return GeneratedManifest{}, fmt.Errorf("fixture manifest %s: %w", path, err)
	}
	if manifest.SchemaVersion >= 8 && manifest.Case.ArchiveFormat == Zip {
		// From schema 8 every zip fixture carries what its archive was found to
		// contain. A zip manifest without it has been edited, or was written by
		// a generator that never parsed the archive back, and either way the
		// zip64 claim in it cannot be trusted.
		if manifest.ZipStructure == nil {
			return GeneratedManifest{}, fmt.Errorf("fixture manifest %s is a zip fixture with no zip_structure record", path)
		}
		if manifest.ZipStructure.Declared != manifest.Case.ZipStructure {
			return GeneratedManifest{}, fmt.Errorf("fixture manifest %s disagrees with itself about zip_structure: case says %q, zip_structure says %q", path, manifest.Case.ZipStructure, manifest.ZipStructure.Declared)
		}
	}
	if manifest.SchemaVersion < 7 {
		// Schema 6 and earlier predate the uuencode lane and the compression
		// record. Every fixture written at those versions was yEnc, and its
		// compression result can be recomputed from the digests it already
		// carries.
		manifest.Case.Encoding = YEncEncoding
		manifest.Encoding = YEncEncoding
		manifest.Compression = manifest.measuredCompression()
		return manifest, nil
	}
	if !manifest.Encoding.Valid() {
		return GeneratedManifest{}, fmt.Errorf("fixture manifest %s has unsupported encoding %q", path, manifest.Encoding)
	}
	if manifest.Case.PostEncodingOrDefault() != manifest.Encoding {
		return GeneratedManifest{}, fmt.Errorf("fixture manifest %s disagrees with itself about encoding: case says %q, manifest says %q", path, manifest.Case.PostEncodingOrDefault(), manifest.Encoding)
	}
	return manifest, nil
}

// measuredCompression derives the writer's result from the digests already in
// the manifest. It is what the generator records, and what the loader
// backfills for manifests written before the field existed.
func (m GeneratedManifest) measuredCompression() CompressionDetails {
	details := CompressionDetails{Method: m.Case.Compression}
	for _, file := range m.ExpectedFiles {
		details.PayloadBytes += file.Size
	}
	for _, file := range m.SourceArchiveFiles {
		details.ArchiveBytes += file.Size
	}
	if details.ArchiveBytes == 0 {
		for _, file := range m.ArchiveFiles {
			details.ArchiveBytes += file.Size
		}
	}
	if details.PayloadBytes > 0 {
		details.Ratio = float64(details.ArchiveBytes) / float64(details.PayloadBytes)
	}
	return details
}

// MeasuredCompression is measuredCompression under the name the generator
// calls it by. It is exported so fixture generation records exactly what the
// loader would have derived.
func (m GeneratedManifest) MeasuredCompression() CompressionDetails {
	return m.measuredCompression()
}

// MinimumPostedBytes is the smallest archive a benchmark fixture may post.
// Below it a download finishes inside the clients' start-up and settle time
// and the comparison measures process launch, not the pipeline. The generator
// refuses to write a manifest under the floor and the controller refuses to
// run one, so a corpus that predates the floor cannot produce a result. The
// floor sits under the 150 MiB movie the defaults produce with room for the
// withheld-volume sets, which post one 32 MiB volume less than they list.
const MinimumPostedBytes int64 = 100 << 20

// PostedBytes is the number of archive bytes the seeder actually posts:
// ArchiveFiles only, since withheld files are listed in the NZB but never
// reach the server.
func (m GeneratedManifest) PostedBytes() int64 {
	var total int64
	for _, file := range m.ArchiveFiles {
		total += file.Size
	}
	return total
}

// ValidatePostedSize fails a fixture that posts fewer than MinimumPostedBytes.
func (m GeneratedManifest) ValidatePostedSize() error {
	if posted := m.PostedBytes(); posted < MinimumPostedBytes {
		return fmt.Errorf("fixture posts %d bytes (%.1f MiB); every benchmark fixture must post at least %d MiB — regenerate the corpus with the current fixturegen defaults", posted, float64(posted)/(1<<20), MinimumPostedBytes>>20)
	}
	return nil
}

// PostedFiles is every file the seeder gives the poster: the posted archive
// files plus the withheld ones, which are listed in the NZB without ever
// reaching the server.
func (m GeneratedManifest) PostedFiles() []FileDigest {
	files := append([]FileDigest(nil), m.ArchiveFiles...)
	return append(files, m.WithheldFiles...)
}

func (m GeneratedManifest) IsWithheld(path string) bool {
	for _, file := range m.WithheldFiles {
		if file.Path == path {
			return true
		}
	}
	return false
}

func postedPaths(files []FileDigest) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func validateNZBFileOrder(manifest GeneratedManifest) error {
	posted := manifest.PostedFiles()
	if len(manifest.NZBFileOrder) != len(posted) {
		return fmt.Errorf("nzb_file_order lists %d files, expected %d", len(manifest.NZBFileOrder), len(posted))
	}
	remaining := make(map[string]int, len(posted))
	for _, file := range posted {
		remaining[file.Path]++
	}
	for _, name := range manifest.NZBFileOrder {
		if remaining[name] == 0 {
			return fmt.Errorf("nzb_file_order names unknown posted file %q", name)
		}
		remaining[name]--
	}
	return nil
}
