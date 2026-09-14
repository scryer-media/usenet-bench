// Package fixture defines the checked-in, reproducible archive fixture matrix.
package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FixturePassword is intentionally public: these archives exercise encrypted
// archive handling, rather than password discovery. It must never be reused
// outside this benchmark corpus.
const FixturePassword = "nntp-bench-fixture-password"

// ArchiveFormat is the on-wire container family a fixture is written in. It
// is separate from the writer release that produced it: RAR 6 and 7 are newer
// writers of the same RAR5 container.
type ArchiveFormat string

const (
	// RAR4 is the legacy pre-RAR5 family emitted by the source-locked 3.93
	// and 4.20 writers.
	RAR4 ArchiveFormat = "rar4"
	// RAR5 is the on-wire format introduced by RAR 5.x and used by 5.x-7.x.
	RAR5 ArchiveFormat = "rar5"
	// SevenZip is the 7z container written by the official 7-Zip console
	// build. Its multi-volume members are named fixture.7z.001, .002, ...
	SevenZip ArchiveFormat = "7z"
	// Tar is the GNU tar container, posted plain or through gzip or xz. It
	// carries no per-member checksum and cannot split itself, so a tar lane
	// is a single posted file unless something else wraps it.
	Tar ArchiveFormat = "tar"
	// XZCompressed is a bare .xz stream over a single media file: a
	// container-less compressor, with no member list at all.
	XZCompressed ArchiveFormat = "xz"
	// Zip is the ZIP container. It is the one format in the corpus with two
	// materially different upstream writers in common use, so the matrix
	// writes it with both Info-ZIP and the pinned 7-Zip build.
	Zip ArchiveFormat = "zip"
	// Media is the absence of a container: the payload files are posted as
	// they were rendered. It is what a direct download or an SFV-only post
	// looks like on the wire.
	Media ArchiveFormat = "media"
)

func (f ArchiveFormat) Valid() bool {
	switch f {
	case RAR4, RAR5, SevenZip, Tar, XZCompressed, Zip, Media:
		return true
	default:
		return false
	}
}

// SupportsVolumes reports whether the writer for this format can split its
// own output into posted parts. tar, xz and a bare media post cannot; a zip
// can, but only when the set asks for it, so zip is handled by whether the
// set declares a volume size rather than by the format alone.
func (f ArchiveFormat) SupportsVolumes() bool {
	switch f {
	case RAR4, RAR5, SevenZip, Zip:
		return true
	default:
		return false
	}
}

// RequiresVolumes reports whether the format has no single-file form in this
// corpus: the RAR and 7z writers are always driven multi-volume, because that
// is what a real post of them looks like.
func (f ArchiveFormat) RequiresVolumes() bool {
	switch f {
	case RAR4, RAR5, SevenZip:
		return true
	default:
		return false
	}
}

// SupportsSolid reports whether the format has a solid mode at all. Only the
// RAR and 7z writers do; tar, xz, zip and a bare media post have no notion of
// compressing members as one stream.
func (f ArchiveFormat) SupportsSolid() bool {
	switch f {
	case RAR4, RAR5, SevenZip:
		return true
	default:
		return false
	}
}

// AllowsEncryption reports which encryption modes the format has. ZIP has
// encrypted data (ZipCrypto in Info-ZIP, AES in 7-Zip) but no encrypted
// header directory; tar, xz and media have neither.
func (f ArchiveFormat) AllowsEncryption(encryption Encryption) bool {
	switch encryption {
	case NoEncryption:
		return true
	case DataEncryption:
		return f == RAR4 || f == RAR5 || f == SevenZip || f == Zip
	case HeaderEncryption:
		return f == RAR4 || f == RAR5 || f == SevenZip
	default:
		return false
	}
}

// AllowsCompression reports whether the format's pinned writer can be driven
// with this method. It is checked when the matrix loads, so a nonsense pair
// like a gzipped 7z or a PPMd zip is an authoring error rather than a
// generation-time surprise hours into a corpus build.
func (f ArchiveFormat) AllowsCompression(compression Compression) bool {
	switch f {
	case RAR4, RAR5:
		return compression == Store || compression == Normal || compression == Best
	case SevenZip:
		switch compression {
		case Store, Normal, Best, LZMACompression, LZMA2Compression, PPMdCompression, BZip2Compression:
			return true
		}
		return false
	case Tar:
		return compression == Store || compression == GzipCompression || compression == XZCompression
	case XZCompressed:
		return compression == XZCompression
	case Zip:
		return compression == Store || compression == DeflateCompression
	case Media:
		return compression == Store
	default:
		return false
	}
}

// ZipStructure names which of the ZIP container's structural variants a zip
// lane is written in. It is a ZIP-only axis: zip64 records and streamed data
// descriptors are structures of that container and nothing else in the corpus
// has them, so naming one on another format is an authoring mistake rather
// than a generation-time surprise.
//
// It is declared in the matrix, but it is never what the manifest reports: the
// generated fixture's zip is read back and parsed, and the manifest records
// what the bytes actually carry (see ZipStructureDetails). A lane whose writer
// did not produce the declared structure is refused, not posted.
type ZipStructure string

const (
	// ClassicZipStructure is the ordinary ZIP every other zip lane carries:
	// 32-bit sizes and offsets, and each member's sizes written in its local
	// header because the writer could seek back and patch them.
	ClassicZipStructure ZipStructure = ""
	// ForcedZip64Structure asks Info-ZIP for zip64 records over a payload that
	// does not need them (`-fz`): a zip64 end-of-central-directory record, its
	// locator, and a zip64 extra field on every entry. It tests the zip64
	// structures themselves at a size a client meets every day, which is a
	// different test from meeting them because a member grew past 4 GiB.
	ForcedZip64Structure ZipStructure = "zip64-forced"
	// LargeZip64Structure is a zip64 archive that had to be one: a single
	// member past 4 GiB, so the 32-bit size fields hold 0xFFFFFFFF and the only
	// true size is in the extra field. 7-Zip's ZIP writer switches to zip64 on
	// its own there, and it has no switch to do so anywhere else.
	LargeZip64Structure ZipStructure = "zip64-large"
	// StreamedZipStructure is a zip written into a pipe. The writer cannot seek
	// back to patch a local header it has already emitted, so it sets general
	// purpose bit 3 on every member and writes the CRC and sizes in a trailing
	// data descriptor instead. That is a separate parser path from a zip whose
	// local headers are complete, and no other lane exercises it.
	StreamedZipStructure ZipStructure = "streamed"
)

func (s ZipStructure) Valid() bool {
	switch s {
	case ClassicZipStructure, ForcedZip64Structure, LargeZip64Structure, StreamedZipStructure:
		return true
	default:
		return false
	}
}

// RequiresZip64 reports whether the declared structure is one the generated
// archive must actually carry zip64 records for. The streamed lane is not one
// of them: the pinned Info-ZIP writer cannot produce a well-formed zip64
// archive into a pipe (see the README's "Zip64 and streamed zips").
func (s ZipStructure) RequiresZip64() bool {
	return s == ForcedZip64Structure || s == LargeZip64Structure
}

// RequiresDataDescriptors reports whether every member of the generated
// archive must set general purpose bit 3.
func (s ZipStructure) RequiresDataDescriptors() bool {
	return s == StreamedZipStructure
}

// NZBOrder declares the order in which a fixture's posted files appear in its
// NZB. Real posts are not always in volume order, and a client that schedules
// by archive need rather than by NZB order behaves differently on the two.
type NZBOrder string

const (
	// SequentialNZBOrder posts archive files in sorted volume order and
	// leaves repair material trailing. It is the default.
	SequentialNZBOrder NZBOrder = "sequential"
	// ScatteredNZBOrder posts archive files in a deterministic pseudo-random
	// permutation, so no volume is guaranteed to arrive first and repair
	// material is interleaved among the volumes.
	ScatteredNZBOrder NZBOrder = "scattered"
)

func (o NZBOrder) Valid() bool {
	return o == SequentialNZBOrder || o == ScatteredNZBOrder
}

// Compression is the method the writer is driven with. The names are the
// harness's, not any one writer's: Normal and Best mean "the release-shaped
// middle setting" and "the writer's maximum", and the named codecs mean
// exactly the codec they name.
type Compression string

const (
	Store  Compression = "store"
	Normal Compression = "normal"
	// Best is the writer's maximum setting with the largest dictionary the
	// pinned build offers for the declared container.
	Best Compression = "best"
	// GzipCompression and XZCompression are the two filters the tar lanes are
	// piped through, and XZCompression is also the whole of the bare .xz lane.
	GzipCompression Compression = "gzip"
	XZCompression   Compression = "xz"
	// DeflateCompression is the ZIP method every ZIP reader has; a stored zip
	// uses Store.
	DeflateCompression Compression = "deflate"
	// The named 7z codecs. 7-Zip's default is LZMA2, so Normal on a 7z lane
	// is deliberately left as the writer default and these are the explicit
	// alternatives a real post can carry.
	LZMACompression  Compression = "lzma"
	LZMA2Compression Compression = "lzma2"
	PPMdCompression  Compression = "ppmd"
	BZip2Compression Compression = "bzip2"
)

func (c Compression) Valid() bool {
	switch c {
	case Store, Normal, Best, GzipCompression, XZCompression, DeflateCompression,
		LZMACompression, LZMA2Compression, PPMdCompression, BZip2Compression:
		return true
	default:
		return false
	}
}

// FixtureClass says what a fixture's result is for. It is carried from the
// matrix through the manifest into every artifact so the summarizer can
// aggregate the two kinds separately: a headline fixture stands for the bulk
// of real posts (a stored multi-volume RAR of already-compressed media, posted
// clear or encrypted, and PAR2 repair over that same stored form), a breadth
// fixture proves a client handles a shape it will meet less often. Pooling
// them equally would let compatibility breadth outvote the common case in the
// published figure.
type FixtureClass string

const (
	HeadlineFixtureClass FixtureClass = "headline"
	BreadthFixtureClass  FixtureClass = "breadth"
)

func (c FixtureClass) Valid() bool {
	return c == HeadlineFixtureClass || c == BreadthFixtureClass
}

type Encryption string

const (
	NoEncryption     Encryption = "none"
	DataEncryption   Encryption = "data"
	HeaderEncryption Encryption = "headers"
)

// PostEncoding is how a fixture's bytes are encoded into article bodies.
// Everything on modern Usenet is yEnc; uuencode is what the medium used
// before it, and it still turns up in old posts and in backfill. It is
// carried as a labelled dimension rather than a stratum: a uuencoded post is
// a different fixture, so it is never pooled with a yEnc one anyway, but the
// label makes "this client cannot read uuencode" legible in a summary.
type PostEncoding string

const (
	YEncEncoding     PostEncoding = "yenc"
	UUEncodeEncoding PostEncoding = "uuencode"
)

func (e PostEncoding) Valid() bool {
	return e == YEncEncoding || e == UUEncodeEncoding
}

// Sidecar is an extra non-archive file posted alongside the payload. It is
// not part of the archive and no client needs it to produce the expected
// output; whether a client consults it at all is the observation.
type Sidecar string

const (
	NoSidecar Sidecar = ""
	// SFVSidecar posts a Simple File Verification list: one CRC32 per posted
	// file, the checksum sidecar that predates PAR2 and still ships with
	// plenty of posts.
	SFVSidecar Sidecar = "sfv"
)

func (s Sidecar) Valid() bool {
	return s == NoSidecar || s == SFVSidecar
}

// InnerArchive declares a second container between the payload and the posted
// archive. Real posts do this: a stored RAR set whose single member is a
// compressed tarball is a common shape for anything that is not video.
type InnerArchive string

const (
	NoInnerArchive InnerArchive = ""
	// TarXZInnerArchive is a .tar.xz written by the pinned GNU tools and then
	// stored, unmodified, by the outer writer.
	TarXZInnerArchive InnerArchive = "tar.xz"
)

func (i InnerArchive) Valid() bool {
	return i == NoInnerArchive || i == TarXZInnerArchive
}

// FileName is the name the inner archive is stored under inside the outer
// container, and therefore the name a client that unpacks only one level is
// left holding.
func (i InnerArchive) FileName() string {
	if i == TarXZInnerArchive {
		return "payload.tar.xz"
	}
	return ""
}

type PayloadKind string

const (
	IncompressiblePayload PayloadKind = "incompressible"
	CompressiblePayload   PayloadKind = "compressible"
)

// PayloadLayout controls the topology of the source files inside an archive.
// It is separate from PayloadKind so a workload can describe both its file
// shape and whether its bulk data compresses.
type PayloadLayout string

const (
	UniformPayloadLayout    PayloadLayout = "uniform"
	BluRayDiscPayloadLayout PayloadLayout = "bluray-disc"
	// Zip64LargePayloadLayout writes exactly one media file past 4 GiB: the
	// size zip64 exists for, where a container's 32-bit size and offset fields
	// hold 0xFFFFFFFF and the truth lives only in an extra field. It is a
	// layout of its own rather than a declared bytes_per_file because its size
	// comes from its own flag, so a smoke run can shrink it without shrinking
	// every other lane in the corpus — and a lane shrunk under 4 GiB is no
	// longer zip64, which the structure inspector refuses rather than posting a
	// classic zip under a zip64 name.
	Zip64LargePayloadLayout PayloadLayout = "zip64-large"
)

// RepairProfile declares a deliberately damaged corpus member and the
// independent repair material posted with it. It is a test dimension, not a
// statement about how frequently any profile appears on Usenet.
type RepairProfile string

const (
	CleanRepairProfile            RepairProfile = "clean"
	PAR2LightRepairProfile        RepairProfile = "par2-light"
	PAR2HeavyRepairProfile        RepairProfile = "par2-heavy"
	RARRecoveryVolumeLightProfile RepairProfile = "rar-recovery-volume-light"
	RARRecoveryVolumeHeavyProfile RepairProfile = "rar-recovery-volume-heavy"
	// PAR2HeavyWithheldProfile posts the same PAR2 material as par2-heavy,
	// but the absent volume is still listed in the NZB with article
	// identifiers that were never posted. That is what a real short post
	// looks like to a client: the file is requested and every article for it
	// is refused, rather than the file simply never being mentioned.
	PAR2HeavyWithheldProfile RepairProfile = "par2-heavy-withheld"

	// The PAR3 profiles are written by the official par3cmdline reference.
	// PAR3 is a large specification and the corpus deliberately covers only
	// the shapes a downloader meets first, so that each lane is a stable
	// release-over-release timing rather than a conformance suite.
	//
	// PAR3LightRepairProfile and PAR3HeavyWithheldProfile mirror their PAR2
	// counterparts exactly — same redundancy, same block size, same fault —
	// with Cauchy Reed-Solomon, the reference's default code, so a PAR3 lane
	// and its PAR2 sibling over the same archive shape differ only in the
	// parity format.
	PAR3LightRepairProfile   RepairProfile = "par3-light"
	PAR3HeavyWithheldProfile RepairProfile = "par3-heavy-withheld"
	// PAR3FFTHeavyWithheldProfile uses the FFT Reed-Solomon code (Leopard)
	// and a compound fault: one input file is withheld and a second is
	// damaged in place, so one repair has to reconstruct a whole file and
	// patch another from the same recovery blocks.
	PAR3FFTHeavyWithheldProfile RepairProfile = "par3-fft-heavy-withheld"
	// PAR3InsideLightProfile embeds the PAR3 packets inside the ZIP or 7z
	// container itself ("PAR inside ZIP"), so the post carries no parity file
	// at all, and damages the member data in several places.
	PAR3InsideLightProfile RepairProfile = "par3-inside-light"

	// The scattered profiles are the regime PAR3's FFT code was designed for:
	// many small blocks and many of them missing. Every block is exactly one
	// article, and the fault is individual articles refused by the server,
	// spread evenly over the whole post, which is how a large real post
	// degrades. PAR2 and PAR3 lanes of the same severity withhold the very
	// same articles with the same redundancy, so the pair differs only in the
	// parity format. PAR2 is at its best geometry here, one block per article
	// and well under its 32768-block limit; its repair work still grows with
	// present blocks times missing blocks, while the FFT code's grows with
	// the block count alone.
	PAR2ScatteredLightProfile    RepairProfile = "par2-scattered-light"
	PAR2ScatteredHeavyProfile    RepairProfile = "par2-scattered-heavy"
	PAR3FFTScatteredLightProfile RepairProfile = "par3-fft-scattered-light"
	PAR3FFTScatteredHeavyProfile RepairProfile = "par3-fft-scattered-heavy"

	// PAR3FFTPastPAR2CapProfile is the lane PAR2 cannot serve at all. The
	// post has more articles than PAR2's 32768-block limit, so any PAR2 set
	// over it needs blocks longer than one article and every withheld article
	// damages about two PAR2 blocks; at the heavy severity's 10% redundancy no
	// PAR2 set of that size can repair the damage, while PAR3 keeps one block
	// per article and repairs it with room to spare. The generator proves
	// both halves: the reference PAR3 repair, and the exact count of PAR2
	// blocks the same articles would damage at PAR2's smallest legal block
	// size, refusing the fixture unless that count exceeds PAR2's recovery.
	// The FFT set is also split into two interleaved cohorts, which the
	// reference does unprompted only above 65536 blocks.
	PAR3FFTPastPAR2CapProfile RepairProfile = "par3-fft-past-par2-cap"
)

// PAR2BlockLimit is the most source blocks one PAR2 recovery set can hold.
const PAR2BlockLimit = 32768

// ScatteredArticleBytes is the raw article size the scattered profiles are
// built for, the 750k article stratum. Their repair blocks are cut to exactly
// this size so one missing article is one missing block; a seed at any other
// article size would misplace every withheld article and is refused.
const ScatteredArticleBytes = 768000

func (p RepairProfile) Valid() bool {
	switch p {
	case CleanRepairProfile, PAR2LightRepairProfile, PAR2HeavyRepairProfile, PAR2HeavyWithheldProfile, RARRecoveryVolumeLightProfile, RARRecoveryVolumeHeavyProfile,
		PAR3LightRepairProfile, PAR3HeavyWithheldProfile, PAR3FFTHeavyWithheldProfile, PAR3InsideLightProfile,
		PAR2ScatteredLightProfile, PAR2ScatteredHeavyProfile, PAR3FFTScatteredLightProfile, PAR3FFTScatteredHeavyProfile,
		PAR3FFTPastPAR2CapProfile:
		return true
	default:
		return false
	}
}

// UsesPAR3 reports whether the profile creates PAR3 recovery material, as
// separate files or embedded in the container.
func (p RepairProfile) UsesPAR3() bool {
	switch p {
	case PAR3LightRepairProfile, PAR3HeavyWithheldProfile, PAR3FFTHeavyWithheldProfile, PAR3InsideLightProfile,
		PAR3FFTScatteredLightProfile, PAR3FFTScatteredHeavyProfile, PAR3FFTPastPAR2CapProfile:
		return true
	default:
		return false
	}
}

// WithholdsArticles reports whether the profile's fault is individual
// articles refused by the server rather than whole files or flipped bytes.
func (p RepairProfile) WithholdsArticles() bool {
	_, _, ok := p.ScatteredDamage()
	return ok
}

// ScatteredDamage is the severity of a scattered profile: how many articles
// in every thousand are withheld, and the redundancy written to repair them.
// Light and heavy are shared by the PAR2 and PAR3 lanes, so both formats see
// the same fault with the same recovery budget.
func (p RepairProfile) ScatteredDamage() (withheldPerMille, redundancyPercent int, ok bool) {
	switch p {
	case PAR2ScatteredLightProfile, PAR3FFTScatteredLightProfile:
		return 10, 5, true
	case PAR2ScatteredHeavyProfile, PAR3FFTScatteredHeavyProfile, PAR3FFTPastPAR2CapProfile:
		return 70, 10, true
	default:
		return 0, 0, false
	}
}

// ExceedsPAR2BlockLimit reports whether the profile's post must be too large
// for a PAR2 set with one block per article.
func (p RepairProfile) ExceedsPAR2BlockLimit() bool {
	return p == PAR3FFTPastPAR2CapProfile
}

// EmbedsPAR3 reports whether the PAR3 packets live inside the container.
func (p RepairProfile) EmbedsPAR3() bool {
	return p == PAR3InsideLightProfile
}

// UsesPAR2 reports whether the profile posts PAR2 recovery material.
func (p RepairProfile) UsesPAR2() bool {
	switch p {
	case PAR2LightRepairProfile, PAR2HeavyRepairProfile, PAR2HeavyWithheldProfile, PAR2ScatteredLightProfile, PAR2ScatteredHeavyProfile:
		return true
	default:
		return false
	}
}

// UsesRARRecoveryVolumes reports whether the profile posts RAR .rev recovery
// volumes, which only the RAR writers can produce.
func (p RepairProfile) UsesRARRecoveryVolumes() bool {
	return p == RARRecoveryVolumeLightProfile || p == RARRecoveryVolumeHeavyProfile
}

// Matrix is the stable source definition. It expands to one ArchiveCase for
// every useful combination, so reviewers can see which cases exist without
// committing any large archive data.
type Matrix struct {
	SchemaVersion int          `json:"schema_version"`
	Description   string       `json:"description"`
	Sets          []FixtureSet `json:"sets"`
}

type FixtureSet struct {
	ID        string `json:"id"`
	WriterEra string `json:"writer_era"`
	// Class is required: every set declares whether it is a headline or a
	// breadth fixture, so the choice is reviewable in the matrix and cannot be
	// made by the summarizer after the fact.
	Class FixtureClass `json:"class"`
	// GeneratorToolchain is the pinned RARLAB image used for this set. It
	// always supplies the FFmpeg payload renderer, and for the RAR lanes it
	// is also the archive writer.
	GeneratorToolchain string `json:"generator_toolchain"`
	// ArchiveWriter names the pinned toolchain that writes the container when
	// it is not the RARLAB image — the official 7-Zip build for the 7z lane.
	// It defaults to GeneratorToolchain.
	ArchiveWriter  string          `json:"archive_writer,omitempty"`
	ArchiveFormat  ArchiveFormat   `json:"archive_format"`
	Compressions   []Compression   `json:"compressions"`
	Solid          []bool          `json:"solid"`
	Encryptions    []Encryption    `json:"encryptions"`
	Payloads       []PayloadKind   `json:"payloads"`
	PayloadLayout  PayloadLayout   `json:"payload_layout,omitempty"`
	RepairProfiles []RepairProfile `json:"repair_profiles,omitempty"`
	NZBOrder       NZBOrder        `json:"nzb_order,omitempty"`
	// QuickOpen keeps the RAR5 writer's quick-open records. The matrix
	// suppresses them by default (`-qo-`), so a set that carries them is a
	// deliberate lane for the readers that consult them: clients that
	// cross-check the quick-open copy of the file headers against the
	// physical header walk only exercise that path on this shape.
	QuickOpen bool `json:"quick_open,omitempty"`
	// TextCompression forces the RAR4 writer's PPMd text model on with
	// `-mc+t` instead of letting the writer choose per file. RAR5 dropped
	// PPMd entirely, so this is a RAR4-only lane by construction.
	TextCompression bool `json:"text_compression,omitempty"`
	// InnerArchive wraps the payload in a second container before the outer
	// writer stores it.
	InnerArchive InnerArchive `json:"inner_archive,omitempty"`
	// ZipStructure selects a structural variant of the ZIP container: zip64
	// records, or streamed data descriptors. Empty is the ordinary zip the
	// other zip lanes carry.
	ZipStructure ZipStructure `json:"zip_structure,omitempty"`
	// Sidecar posts an extra checksum file alongside the archive.
	Sidecar Sidecar `json:"sidecar,omitempty"`
	// Encoding is how the articles are encoded. It defaults to yEnc, which is
	// what every posting tool in use writes.
	Encoding  PostEncoding `json:"encoding,omitempty"`
	FileCount int          `json:"file_count"`
	// BytesPerFile overrides the generator's per-payload-file size for this
	// set, written the way volume sizes are ("224m"). Empty keeps the
	// generator default for the payload kind and file count.
	BytesPerFile string `json:"bytes_per_file,omitempty"`
	// VolumeSize is the writer's split size. It is required for the formats
	// that always post multi-volume and must be empty for the formats that
	// cannot split themselves at all.
	VolumeSize string `json:"volume_size"`
}

// ArchiveCase is one materialized archive fixture.
type ArchiveCase struct {
	ID                 string        `json:"id"`
	SetID              string        `json:"set_id"`
	Class              FixtureClass  `json:"class"`
	WriterEra          string        `json:"writer_era"`
	GeneratorToolchain string        `json:"generator_toolchain"`
	ArchiveWriter      string        `json:"archive_writer"`
	ArchiveFormat      ArchiveFormat `json:"archive_format"`
	Compression        Compression   `json:"compression"`
	Solid              bool          `json:"solid"`
	Encryption         Encryption    `json:"encryption"`
	Payload            PayloadKind   `json:"payload"`
	PayloadLayout      PayloadLayout `json:"payload_layout"`
	RepairProfile      RepairProfile `json:"repair_profile"`
	NZBOrder           NZBOrder      `json:"nzb_order"`
	QuickOpen          bool          `json:"quick_open,omitempty"`
	TextCompression    bool          `json:"text_compression,omitempty"`
	InnerArchive       InnerArchive  `json:"inner_archive,omitempty"`
	ZipStructure       ZipStructure  `json:"zip_structure,omitempty"`
	Sidecar            Sidecar       `json:"sidecar,omitempty"`
	Encoding           PostEncoding  `json:"encoding"`
	FileCount          int           `json:"file_count"`
	BytesPerFile       string        `json:"bytes_per_file,omitempty"`
	VolumeSize         string        `json:"volume_size"`
}

// ByteSize parses a writer-style size such as "32m", "224m" or "1024k" into
// bytes. It is the same spelling the RAR and 7-Zip volume switches take, so
// the matrix has one way of writing a size rather than two.
func ByteSize(text string) (int64, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("size is empty")
	}
	multiplier := int64(1)
	switch trimmed[len(trimmed)-1] {
	case 'k', 'K':
		multiplier = 1 << 10
	case 'm', 'M':
		multiplier = 1 << 20
	case 'g', 'G':
		multiplier = 1 << 30
	}
	digits := trimmed
	if multiplier != 1 {
		digits = trimmed[:len(trimmed)-1]
	}
	value, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid size %q", text)
	}
	if value > (1<<62)/multiplier {
		return 0, fmt.Errorf("size %q overflows", text)
	}
	return value * multiplier, nil
}

func LoadMatrix(path string) (Matrix, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Matrix{}, fmt.Errorf("read fixture matrix %s: %w", path, err)
	}

	var matrix Matrix
	if err := json.Unmarshal(contents, &matrix); err != nil {
		return Matrix{}, fmt.Errorf("decode fixture matrix %s: %w", path, err)
	}
	if err := matrix.Validate(); err != nil {
		return Matrix{}, err
	}
	return matrix, nil
}

func (m Matrix) Validate() error {
	if m.SchemaVersion != 2 {
		return fmt.Errorf("unsupported fixture matrix schema version %d", m.SchemaVersion)
	}
	if len(m.Sets) == 0 {
		return fmt.Errorf("fixture matrix has no sets")
	}
	_, err := m.Expand()
	return err
}

// Expand deterministically materializes the cartesian product for every set.
func (m Matrix) Expand() ([]ArchiveCase, error) {
	ids := make(map[string]struct{})
	cases := make([]ArchiveCase, 0)
	for _, set := range m.Sets {
		if err := set.validate(); err != nil {
			return nil, err
		}
		layout := set.PayloadLayout
		if layout == "" {
			layout = UniformPayloadLayout
		}
		profiles := set.RepairProfiles
		if len(profiles) == 0 {
			profiles = []RepairProfile{CleanRepairProfile}
		}
		order := set.NZBOrder
		if order == "" {
			order = SequentialNZBOrder
		}
		writer := set.ArchiveWriter
		if writer == "" {
			writer = set.GeneratorToolchain
		}
		encoding := set.Encoding
		if encoding == "" {
			encoding = YEncEncoding
		}
		for _, profile := range profiles {
			for _, compression := range set.Compressions {
				for _, solid := range set.Solid {
					for _, encryption := range set.Encryptions {
						for _, payload := range set.Payloads {
							parts := []string{
								set.ID,
							}
							if profile != CleanRepairProfile {
								parts = append(parts, string(profile))
							}
							parts = append(parts,
								string(compression),
								solidID(solid),
								string(encryption),
								string(payload),
							)
							id := strings.Join(parts, "-")
							if _, exists := ids[id]; exists {
								return nil, fmt.Errorf("fixture matrix has duplicate case %q", id)
							}
							ids[id] = struct{}{}
							cases = append(cases, ArchiveCase{
								ID:                 id,
								SetID:              set.ID,
								Class:              set.Class,
								WriterEra:          set.WriterEra,
								GeneratorToolchain: set.GeneratorToolchain,
								ArchiveWriter:      writer,
								ArchiveFormat:      set.ArchiveFormat,
								Compression:        compression,
								Solid:              solid,
								Encryption:         encryption,
								Payload:            payload,
								PayloadLayout:      layout,
								RepairProfile:      profile,
								NZBOrder:           order,
								QuickOpen:          set.QuickOpen,
								TextCompression:    set.TextCompression,
								InnerArchive:       set.InnerArchive,
								ZipStructure:       set.ZipStructure,
								Sidecar:            set.Sidecar,
								Encoding:           encoding,
								FileCount:          set.FileCount,
								BytesPerFile:       set.BytesPerFile,
								VolumeSize:         set.VolumeSize,
							})
						}
					}
				}
			}
		}
	}
	return cases, nil
}

func (s FixtureSet) validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("fixture set has an empty id")
	}
	if strings.TrimSpace(s.WriterEra) == "" {
		return fmt.Errorf("fixture set %q has an empty writer_era", s.ID)
	}
	if !s.Class.Valid() {
		return fmt.Errorf("fixture set %q must declare class %q or %q, got %q", s.ID, HeadlineFixtureClass, BreadthFixtureClass, s.Class)
	}
	if strings.TrimSpace(s.GeneratorToolchain) == "" {
		return fmt.Errorf("fixture set %q has an empty generator_toolchain", s.ID)
	}
	if !s.ArchiveFormat.Valid() {
		return fmt.Errorf("fixture set %q has unsupported archive_format %q", s.ID, s.ArchiveFormat)
	}
	if s.NZBOrder != "" && !s.NZBOrder.Valid() {
		return fmt.Errorf("fixture set %q has unsupported nzb_order %q", s.ID, s.NZBOrder)
	}
	// Quick-open records are a RAR5 container feature; the legacy writers
	// reject the switch and 7-Zip has no equivalent.
	if s.QuickOpen && s.ArchiveFormat != RAR5 {
		return fmt.Errorf("fixture set %q cannot set quick_open: quick-open records are RAR5-only", s.ID)
	}
	// RAR5 has no PPMd and no LZMA mode: RARLAB removed the text model when
	// the format changed. Forcing it on a RAR5 set would silently produce a
	// lane that is not what it says it is.
	if s.TextCompression && s.ArchiveFormat != RAR4 {
		return fmt.Errorf("fixture set %q cannot set text_compression: PPMd text compression is a RAR4-only feature", s.ID)
	}
	if s.Encoding != "" && !s.Encoding.Valid() {
		return fmt.Errorf("fixture set %q has unsupported encoding %q", s.ID, s.Encoding)
	}
	// uuencode has no member list and no per-file framing: it encodes one
	// stream of bytes. The corpus therefore only posts it over a bare media
	// fixture, where the posted file is the payload itself.
	if s.Encoding == UUEncodeEncoding && s.ArchiveFormat != Media {
		return fmt.Errorf("fixture set %q cannot post %q: uuencode is only used for the container-less media lane", s.ID, s.Encoding)
	}
	if !s.Sidecar.Valid() {
		return fmt.Errorf("fixture set %q has unsupported sidecar %q", s.ID, s.Sidecar)
	}
	if !s.InnerArchive.Valid() {
		return fmt.Errorf("fixture set %q has unsupported inner_archive %q", s.ID, s.InnerArchive)
	}
	// A wrapper only makes sense when the outer writer stores what it is
	// handed: compressing an already-compressed inner archive would measure
	// the outer codec against noise instead of the shape being tested.
	if s.InnerArchive != NoInnerArchive {
		if !s.ArchiveFormat.SupportsVolumes() || s.ArchiveFormat == Zip {
			return fmt.Errorf("fixture set %q cannot set inner_archive: only the RAR and 7z lanes wrap another container", s.ID)
		}
		for _, compression := range s.Compressions {
			if compression != Store {
				return fmt.Errorf("fixture set %q must store its inner_archive, got compression %q", s.ID, compression)
			}
		}
	}
	if err := s.validateZipStructure(); err != nil {
		return err
	}
	if s.BytesPerFile != "" {
		if _, err := ByteSize(s.BytesPerFile); err != nil {
			return fmt.Errorf("fixture set %q has invalid bytes_per_file %q: %w", s.ID, s.BytesPerFile, err)
		}
	}
	if len(s.Compressions) == 0 || len(s.Solid) == 0 || len(s.Encryptions) == 0 || len(s.Payloads) == 0 {
		return fmt.Errorf("fixture set %q must specify every matrix axis", s.ID)
	}
	for _, profile := range s.RepairProfiles {
		if !profile.Valid() {
			return fmt.Errorf("fixture set %q has unsupported repair_profile %q", s.ID, profile)
		}
		// RAR recovery volumes are a RAR container feature. 7-Zip has no
		// equivalent, so pairing them is a matrix authoring mistake rather
		// than a generation-time surprise.
		if s.ArchiveFormat != RAR4 && s.ArchiveFormat != RAR5 && profile.UsesRARRecoveryVolumes() {
			return fmt.Errorf("fixture set %q cannot use repair_profile %q: RAR recovery volumes are not a %s feature", s.ID, profile, s.ArchiveFormat)
		}
		if err := s.validatePAR3Profile(profile); err != nil {
			return err
		}
		// Articles are withheld by rewriting the NZB the yEnc poster emits;
		// the uuencode seeder has no such step and would post them all.
		if profile.WithholdsArticles() && s.Encoding != "" && s.Encoding != YEncEncoding {
			return fmt.Errorf("fixture set %q cannot use repair_profile %q with encoding %q: withheld articles are a yEnc posting feature", s.ID, profile, s.Encoding)
		}
	}
	if s.PayloadLayout == "" {
		s.PayloadLayout = UniformPayloadLayout
	}
	if s.PayloadLayout != UniformPayloadLayout && s.PayloadLayout != BluRayDiscPayloadLayout && s.PayloadLayout != Zip64LargePayloadLayout {
		return fmt.Errorf("fixture set %q has unsupported payload_layout %q", s.ID, s.PayloadLayout)
	}
	if s.PayloadLayout == UniformPayloadLayout && s.FileCount < 1 {
		return fmt.Errorf("fixture set %q has invalid file_count %d", s.ID, s.FileCount)
	}
	// A format that cannot split its own output has no volume size to
	// declare, and one that is always posted multi-volume must declare one.
	// Saying otherwise in the matrix would put a number in the manifest that
	// no writer ever saw.
	switch {
	case s.ArchiveFormat.RequiresVolumes() && strings.TrimSpace(s.VolumeSize) == "":
		return fmt.Errorf("fixture set %q has an empty volume_size", s.ID)
	case !s.ArchiveFormat.SupportsVolumes() && strings.TrimSpace(s.VolumeSize) != "":
		return fmt.Errorf("fixture set %q declares volume_size %q, but the %s writer cannot split its own output", s.ID, s.VolumeSize, s.ArchiveFormat)
	}
	if strings.TrimSpace(s.VolumeSize) != "" {
		if _, err := ByteSize(s.VolumeSize); err != nil {
			return fmt.Errorf("fixture set %q has invalid volume_size %q: %w", s.ID, s.VolumeSize, err)
		}
	}
	for _, compression := range s.Compressions {
		if !compression.Valid() {
			return fmt.Errorf("fixture set %q has unsupported compression %q", s.ID, compression)
		}
		if !s.ArchiveFormat.AllowsCompression(compression) {
			return fmt.Errorf("fixture set %q cannot use compression %q: the pinned %s writer has no such method", s.ID, compression, s.ArchiveFormat)
		}
	}
	for _, encryption := range s.Encryptions {
		if encryption != NoEncryption && encryption != DataEncryption && encryption != HeaderEncryption {
			return fmt.Errorf("fixture set %q has unsupported encryption %q", s.ID, encryption)
		}
		if !s.ArchiveFormat.AllowsEncryption(encryption) {
			return fmt.Errorf("fixture set %q cannot use encryption %q: the %s container has no such mode", s.ID, encryption, s.ArchiveFormat)
		}
	}
	for _, solid := range s.Solid {
		if solid && !s.ArchiveFormat.SupportsSolid() {
			return fmt.Errorf("fixture set %q cannot be solid: the %s container compresses each member independently", s.ID, s.ArchiveFormat)
		}
	}
	// The RAR lanes are written by the same pinned image that renders their
	// payload, so archive_writer is optional there and defaults to it. Every
	// other container has a separate pinned writer that must be named, so the
	// manifest can never be ambiguous about which upstream wrote the bytes.
	if s.ArchiveFormat != RAR4 && s.ArchiveFormat != RAR5 && strings.TrimSpace(s.ArchiveWriter) == "" {
		return fmt.Errorf("fixture set %q must name the pinned archive_writer for its %s container", s.ID, s.ArchiveFormat)
	}
	for _, profile := range s.RepairProfiles {
		// A checksum sidecar and a repair profile both add non-archive files
		// to the post. Combining them would make it impossible to say which
		// one a client reacted to.
		if s.Sidecar != NoSidecar && profile != CleanRepairProfile {
			return fmt.Errorf("fixture set %q cannot pair sidecar %q with repair_profile %q", s.ID, s.Sidecar, profile)
		}
	}
	for _, payload := range s.Payloads {
		if payload != IncompressiblePayload && payload != CompressiblePayload {
			return fmt.Errorf("fixture set %q has unsupported payload %q", s.ID, payload)
		}
	}
	return nil
}

// validatePAR3Profile refuses the PAR3 pairings the generator cannot build
// faithfully. Embedded PAR3 needs a single, unencrypted, ordinary ZIP or 7z
// file for the reference to insert its packets into. The separate-file
// profiles fault a non-leading input, so a container has to be split and a
// bare-media post has to carry enough files to leave one untouched first file
// plus every faulted one.
func (s FixtureSet) validatePAR3Profile(profile RepairProfile) error {
	if !profile.UsesPAR3() {
		return nil
	}
	if s.InnerArchive != NoInnerArchive {
		return fmt.Errorf("fixture set %q cannot pair inner_archive %q with repair_profile %q", s.ID, s.InnerArchive, profile)
	}
	if profile.EmbedsPAR3() {
		if s.ArchiveFormat != Zip && s.ArchiveFormat != SevenZip {
			return fmt.Errorf("fixture set %q cannot use repair_profile %q: PAR3 packets can only be embedded in a zip or 7z container, not %s", s.ID, profile, s.ArchiveFormat)
		}
		if strings.TrimSpace(s.VolumeSize) != "" {
			return fmt.Errorf("fixture set %q cannot use repair_profile %q with volume_size %q: the embedded packets protect one container file", s.ID, profile, s.VolumeSize)
		}
		if s.ZipStructure != ClassicZipStructure {
			return fmt.Errorf("fixture set %q cannot use repair_profile %q with zip_structure %q", s.ID, profile, s.ZipStructure)
		}
		for _, encryption := range s.Encryptions {
			if encryption != NoEncryption {
				return fmt.Errorf("fixture set %q cannot use repair_profile %q with encryption %q", s.ID, profile, encryption)
			}
		}
		return nil
	}
	if profile.ExceedsPAR2BlockLimit() {
		// Container overhead only adds bytes, so a payload past the limit is
		// a post past it. A reduced-size local run scales the payload down and
		// the generator refuses the fixture then; the matrix must still
		// declare a size that means something.
		size, err := ByteSize(s.BytesPerFile)
		if err != nil || int64(s.FileCount)*size <= PAR2BlockLimit*ScatteredArticleBytes {
			return fmt.Errorf("fixture set %q cannot use repair_profile %q: its payload must exceed %d articles of %d bytes", s.ID, profile, PAR2BlockLimit, ScatteredArticleBytes)
		}
	}
	if profile.WithholdsArticles() {
		// Scattered damage lands anywhere in the post, so it needs neither a
		// split container nor spare files.
		return nil
	}
	if s.ArchiveFormat == Media {
		needed := 2
		if profile == PAR3HeavyWithheldProfile {
			needed = 3
		}
		if profile == PAR3FFTHeavyWithheldProfile {
			needed = 4
		}
		if s.FileCount < needed {
			return fmt.Errorf("fixture set %q needs file_count of at least %d for repair_profile %q, has %d", s.ID, needed, profile, s.FileCount)
		}
		return nil
	}
	if strings.TrimSpace(s.VolumeSize) == "" {
		return fmt.Errorf("fixture set %q must split its %s container to use repair_profile %q", s.ID, s.ArchiveFormat, profile)
	}
	return nil
}

// validateZipStructure refuses every nonsense pairing of the ZIP structural
// axis by name, when the matrix loads rather than hours into a corpus build.
// Which pinned writer can produce a declared structure is checked separately,
// where the toolchain ids are known: see resolveZipWriter in the generator.
func (s FixtureSet) validateZipStructure() error {
	layout := s.PayloadLayout
	if layout == "" {
		layout = UniformPayloadLayout
	}
	if s.ZipStructure != ClassicZipStructure {
		if !s.ZipStructure.Valid() {
			return fmt.Errorf("fixture set %q has unsupported zip_structure %q", s.ID, s.ZipStructure)
		}
		if s.ArchiveFormat != Zip {
			return fmt.Errorf("fixture set %q cannot set zip_structure %q: zip64 records and streamed data descriptors are ZIP container structures, and the %s container has neither", s.ID, s.ZipStructure, s.ArchiveFormat)
		}
		// These lanes exist to test one structure. An encrypted one would make
		// a client's failure two things at once, and the ZipCrypto and AES
		// lanes already cover the ciphers over an ordinary zip.
		for _, encryption := range s.Encryptions {
			if encryption != NoEncryption {
				return fmt.Errorf("fixture set %q cannot pair zip_structure %q with encryption %q: the ZipCrypto and AES zip lanes cover the ciphers, and combining them here would make one failure two things at once", s.ID, s.ZipStructure, encryption)
			}
		}
	}
	switch s.ZipStructure {
	case StreamedZipStructure:
		// The writer only leaves general purpose bit 3 set because it is
		// writing into a pipe it cannot seek back into. A split set is written
		// to files, so asking for both asks for a shape no writer produces.
		if strings.TrimSpace(s.VolumeSize) != "" {
			return fmt.Errorf("fixture set %q cannot pair zip_structure %q with volume_size %q: a streamed zip is written into a pipe and has no split to write", s.ID, s.ZipStructure, s.VolumeSize)
		}
		if layout != UniformPayloadLayout {
			return fmt.Errorf("fixture set %q cannot pair zip_structure %q with payload_layout %q", s.ID, s.ZipStructure, layout)
		}
	case LargeZip64Structure:
		// The spanned zip lane already covers split sets; what this one is for
		// is the single member whose size does not fit in 32 bits.
		if strings.TrimSpace(s.VolumeSize) != "" {
			return fmt.Errorf("fixture set %q cannot pair zip_structure %q with volume_size %q: the spanned zip lane covers split sets, and this one is a single volume holding one member past 4 GiB", s.ID, s.ZipStructure, s.VolumeSize)
		}
		if layout != Zip64LargePayloadLayout {
			return fmt.Errorf("fixture set %q must declare payload_layout %q for zip_structure %q: only that layout writes the member past 4 GiB that zip64 exists for", s.ID, Zip64LargePayloadLayout, s.ZipStructure)
		}
	case ForcedZip64Structure:
		if layout != UniformPayloadLayout {
			return fmt.Errorf("fixture set %q cannot pair zip_structure %q with payload_layout %q: the forced lane exists to carry zip64 records over a payload that does not need them", s.ID, s.ZipStructure, layout)
		}
	}
	// The large-member layout is only ever the zip64 lane's; anywhere else it
	// would post several gigabytes for a shape that does not use them.
	if layout == Zip64LargePayloadLayout {
		if s.ZipStructure != LargeZip64Structure {
			return fmt.Errorf("fixture set %q cannot use payload_layout %q outside zip_structure %q", s.ID, layout, LargeZip64Structure)
		}
		if s.FileCount != 1 {
			return fmt.Errorf("fixture set %q has file_count %d: payload_layout %q writes exactly one member", s.ID, s.FileCount, layout)
		}
	}
	return nil
}

func solidID(solid bool) string {
	if solid {
		return "solid"
	}
	return "nonsolid"
}

// RARArgs returns the RARLAB invocation suffix for this fixture. archive and
// inputs are paths within the container's mounted working directory.
func (c ArchiveCase) RARArgs(archive string, inputs []string) ([]string, error) {
	if archive == "" {
		return nil, fmt.Errorf("fixture %q has an empty archive path", c.ID)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("fixture %q has no input files", c.ID)
	}
	if c.ArchiveFormat != RAR4 && c.ArchiveFormat != RAR5 {
		return nil, fmt.Errorf("fixture %q is a %s case, not a RAR case", c.ID, c.ArchiveFormat)
	}
	args := []string{"a", "-idq", "-y", "-ep1"}
	switch c.ArchiveFormat {
	case RAR4:
		// The source-locked 3.x and 4.x writers predate the -ma selector;
		// their default archive format is the explicitly selected legacy lane.
	case RAR5:
		args = append(args, "-ma5")
		if c.QuickOpen {
			// -qo+ writes a quick-open record for every file header, not
			// just the ones the writer's size heuristic would pick, so the
			// lane is deterministic about carrying them.
			args = append(args, "-qo+")
		} else {
			// Quick-open data speeds listing, but adds bytes to every upload
			// and is not needed by the download/extract clients under test.
			args = append(args, "-qo-")
		}
	default:
		return nil, fmt.Errorf("fixture %q has unsupported archive_format %q", c.ID, c.ArchiveFormat)
	}
	switch c.Compression {
	case Store:
		args = append(args, "-m0")
	case Normal:
		// -m3 is the writer's own default and what a release compressed
		// without thinking about it carries. The dictionary is left at the
		// writer's default for the level, for the same reason.
		args = append(args, "-m3")
	case Best:
		// The maximum compression setting, with the largest dictionary the
		// lane can ask a client to allocate. RAR4 tops out at 4 MiB, so that
		// one is simply the format maximum. RAR5 is a deliberate stop short
		// of the maximum: the pinned 7.23 writer accepts -md1g, but it sizes
		// the stored dictionary to the data (a 280 MiB member written with
		// -md1g records -md=512m), and every extractor then allocates that
		// much to open the archive. A lane that makes a client reserve half a
		// gigabyte measures the host's memory, not its pipeline, so this one
		// stops at 256 MiB, which is also the largest dictionary in common
		// use for released RAR5 sets.
		args = append(args, "-m5")
		switch c.ArchiveFormat {
		case RAR4:
			args = append(args, "-md4096")
		case RAR5:
			args = append(args, "-md256m")
		}
	default:
		return nil, fmt.Errorf("fixture %q has unsupported compression %q", c.ID, c.Compression)
	}
	if c.TextCompression {
		if c.ArchiveFormat != RAR4 {
			return nil, fmt.Errorf("fixture %q cannot force text compression: PPMd is RAR4-only", c.ID)
		}
		// `-mc+t` forces the PPMd text model on for every member instead of
		// leaving the choice to the writer's per-file heuristic, so the lane
		// deterministically exercises the PPMd decoder.
		args = append(args, "-mc+t")
	}
	if c.Solid {
		args = append(args, "-s")
	} else {
		args = append(args, "-s-")
	}
	switch c.Encryption {
	case NoEncryption:
	case DataEncryption:
		args = append(args, "-p"+FixturePassword)
	case HeaderEncryption:
		args = append(args, "-hp"+FixturePassword)
	default:
		return nil, fmt.Errorf("fixture %q has unsupported encryption %q", c.ID, c.Encryption)
	}
	if strings.TrimSpace(c.VolumeSize) != "" {
		args = append(args, "-v"+c.VolumeSize)
	}
	args = append(args, filepath.ToSlash(archive))
	for _, input := range inputs {
		args = append(args, filepath.ToSlash(input))
	}
	return args, nil
}

// SevenZipArgs returns the official 7-Zip console invocation suffix for this
// fixture. archive and inputs are paths relative to the working directory the
// generator gives the 7-Zip container, so stored member names carry no
// staging prefix. Multi-volume output is named archive.001, archive.002, ...
func (c ArchiveCase) SevenZipArgs(archive string, inputs []string) ([]string, error) {
	if c.ArchiveFormat != SevenZip && c.ArchiveFormat != Zip {
		return nil, fmt.Errorf("fixture %q is not a 7-Zip-written case", c.ID)
	}
	if archive == "" {
		return nil, fmt.Errorf("fixture %q has an empty archive path", c.ID)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("fixture %q has no input files", c.ID)
	}
	if c.ArchiveFormat == SevenZip && strings.TrimSpace(c.VolumeSize) == "" {
		return nil, fmt.Errorf("fixture %q has an empty volume_size", c.ID)
	}
	if c.ArchiveFormat == Zip {
		switch c.ZipStructure {
		case ClassicZipStructure, LargeZip64Structure:
			// 7-Zip's ZIP writer switches to zip64 on its own once a member or
			// an offset passes 4 GiB, which is what the large lane relies on.
		default:
			return nil, fmt.Errorf("fixture %q asks 7-Zip for the %q zip structure: 7-Zip has no force-zip64 switch and never streams a zip into a pipe, so only Info-ZIP can write that lane", c.ID, c.ZipStructure)
		}
	}
	// -bso0 and -bsp0 silence the informational and progress streams without
	// hiding errors, which stay on stderr.
	args := []string{"a", "-y", "-bso0", "-bsp0"}
	if c.ArchiveFormat == Zip {
		args = append(args, "-tzip")
	} else {
		args = append(args, "-t7z")
	}
	method, err := c.sevenZipMethodArgs()
	if err != nil {
		return nil, err
	}
	args = append(args, method...)
	if c.ArchiveFormat == SevenZip {
		if c.Solid {
			args = append(args, "-ms=on")
		} else {
			args = append(args, "-ms=off")
		}
	}
	switch c.Encryption {
	case NoEncryption:
	case DataEncryption:
		args = append(args, "-p"+FixturePassword)
		if c.ArchiveFormat == Zip {
			// ZIP's own default is the ancient ZipCrypto stream cipher, which
			// the Info-ZIP lane already covers. 7-Zip's ZIP writer is the
			// common source of AES-encrypted zips, so this lane names AES-256
			// explicitly rather than inheriting a default.
			args = append(args, "-mem=AES256")
		}
	case HeaderEncryption:
		if c.ArchiveFormat == Zip {
			return nil, fmt.Errorf("fixture %q cannot encrypt zip headers: the ZIP central directory is always readable", c.ID)
		}
		args = append(args, "-p"+FixturePassword, "-mhe=on")
	default:
		return nil, fmt.Errorf("fixture %q has unsupported encryption %q", c.ID, c.Encryption)
	}
	if strings.TrimSpace(c.VolumeSize) != "" {
		args = append(args, "-v"+c.VolumeSize)
	}
	args = append(args, filepath.ToSlash(archive))
	for _, input := range inputs {
		args = append(args, filepath.ToSlash(input))
	}
	return args, nil
}

// sevenZipMethodArgs names the member codec explicitly for every lane. A
// fixture that inherited the writer's default would silently change codec the
// day a later 7-Zip release changed its mind, and the manifest would still
// claim the old one.
func (c ArchiveCase) sevenZipMethodArgs() ([]string, error) {
	if c.ArchiveFormat == Zip {
		switch c.Compression {
		case Store:
			return []string{"-mx0", "-mm=Copy"}, nil
		case DeflateCompression:
			return []string{"-mx9", "-mm=Deflate"}, nil
		default:
			return nil, fmt.Errorf("fixture %q has unsupported zip compression %q", c.ID, c.Compression)
		}
	}
	switch c.Compression {
	case Store:
		// -mx0 selects copy mode and -m0=Copy states the member codec
		// explicitly, so a stored 7z fixture cannot silently pick up a
		// different default filter from a later 7-Zip release.
		return []string{"-mx0", "-m0=Copy"}, nil
	case Normal:
		// LZMA2 at the writer's default level, left unnamed on purpose: this
		// is the lane that stands for "whatever 7-Zip does when nobody
		// chooses", and the named-codec lanes below are the alternatives.
		return []string{"-mx5"}, nil
	case Best:
		return []string{"-mx9"}, nil
	case LZMACompression:
		return []string{"-mx5", "-m0=LZMA"}, nil
	case LZMA2Compression:
		return []string{"-mx5", "-m0=LZMA2"}, nil
	case PPMdCompression:
		return []string{"-mx5", "-m0=PPMd"}, nil
	case BZip2Compression:
		return []string{"-mx5", "-m0=BZip2"}, nil
	default:
		return nil, fmt.Errorf("fixture %q has unsupported compression %q", c.ID, c.Compression)
	}
}

// TarArgs returns the GNU tar invocation that writes this fixture's tarball.
// archive is a case-relative output path and inputs are the payload paths
// relative to the staging directory tar is pointed at, so stored member names
// carry no staging prefix.
func (c ArchiveCase) TarArgs(archive, inputDir string, inputs []string) ([]string, error) {
	if c.ArchiveFormat != Tar {
		return nil, fmt.Errorf("fixture %q is not a tar case", c.ID)
	}
	if archive == "" || inputDir == "" {
		return nil, fmt.Errorf("fixture %q has an empty tar path", c.ID)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("fixture %q has no input files", c.ID)
	}
	// --format=gnu and --sort=name keep the member order and header dialect
	// fixed, so the same payload always produces the same tar layout.
	args := []string{"--create", "--format=gnu", "--sort=name"}
	switch c.Compression {
	case Store:
	case GzipCompression:
		args = append(args, "--gzip")
	case XZCompression:
		args = append(args, "--xz")
	default:
		return nil, fmt.Errorf("fixture %q has unsupported tar compression %q", c.ID, c.Compression)
	}
	args = append(args, "--file", filepath.ToSlash(archive), "--directory", filepath.ToSlash(inputDir))
	for _, input := range inputs {
		args = append(args, filepath.ToSlash(input))
	}
	return args, nil
}

// TarFileName is the name a tar fixture's single posted file carries. The
// suffix states the filter, because that is what a client dispatches on.
func (c ArchiveCase) TarFileName() (string, error) {
	switch c.Compression {
	case Store:
		return "fixture.tar", nil
	case GzipCompression:
		return "fixture.tar.gz", nil
	case XZCompression:
		return "fixture.tar.xz", nil
	default:
		return "", fmt.Errorf("fixture %q has unsupported tar compression %q", c.ID, c.Compression)
	}
}

// XZArgs returns the xz invocation for the bare-stream lane. xz replaces the
// file it is given, so the caller stages a copy of the payload under the
// archive directory and lets xz rename it.
func (c ArchiveCase) XZArgs(target string) ([]string, error) {
	if c.ArchiveFormat != XZCompressed {
		return nil, fmt.Errorf("fixture %q is not an xz case", c.ID)
	}
	if c.Compression != XZCompression {
		return nil, fmt.Errorf("fixture %q has unsupported xz compression %q", c.ID, c.Compression)
	}
	// A single worker thread: xz's multi-threaded encoder splits the input
	// into independently compressed blocks, so the output would depend on how
	// many cores the machine building the corpus happens to have.
	return []string{"-9", "--threads=1", "--quiet", "--format=xz", filepath.ToSlash(target)}, nil
}

// ZipArgs returns the Info-ZIP invocation for this fixture. archive is
// relative to the staging directory zip is run from, so stored member names
// carry no prefix, and a declared volume size turns the lane into a spanned
// zip: fixture.z01, fixture.z02, ... with the central directory in the
// trailing fixture.zip.
func (c ArchiveCase) ZipArgs(archive string, inputs []string) ([]string, error) {
	if c.ArchiveFormat != Zip {
		return nil, fmt.Errorf("fixture %q is not a zip case", c.ID)
	}
	if archive == "" {
		return nil, fmt.Errorf("fixture %q has an empty archive path", c.ID)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("fixture %q has no input files", c.ID)
	}
	if err := c.validateInfoZipStructure(); err != nil {
		return nil, err
	}
	// -X drops the extra attribute blocks that would otherwise record the
	// building machine's uid/gid, and -q keeps the writer silent on success.
	args := []string{"-q", "-X"}
	switch c.Compression {
	case Store:
		args = append(args, "-0")
	case DeflateCompression:
		args = append(args, "-9")
	default:
		return nil, fmt.Errorf("fixture %q has unsupported zip compression %q", c.ID, c.Compression)
	}
	switch c.Encryption {
	case NoEncryption:
	case DataEncryption:
		// Info-ZIP only implements the original ZipCrypto stream cipher. That
		// is the point of this lane: it is what the overwhelming majority of
		// encrypted zips in the wild carry, and it is a different code path in
		// every reader from 7-Zip's AES.
		args = append(args, "-P", FixturePassword)
	default:
		return nil, fmt.Errorf("fixture %q cannot use encryption %q with the Info-ZIP writer", c.ID, c.Encryption)
	}
	if c.ZipStructure == ForcedZip64Structure {
		// -fz writes the zip64 end-of-central-directory record, its locator and
		// a zip64 extra field on every entry, whatever the sizes are. It is the
		// only way to reach those structures without posting a member past
		// 4 GiB, and 7-Zip has no equivalent switch at all.
		args = append(args, "-fz")
	}
	if strings.TrimSpace(c.VolumeSize) != "" {
		// -sb and -sp would make zip prompt for media changes; neither is
		// wanted here, and their absence keeps the run non-interactive.
		args = append(args, "-s", c.VolumeSize)
	}
	if c.ZipStructure == StreamedZipStructure {
		// "-" is the archive name that makes zip stream to standard output.
		// The caller keeps what comes out of the pipe: because zip cannot seek
		// back into one, it leaves general purpose bit 3 set on every local
		// header and writes the CRC and sizes in a trailing data descriptor,
		// which is the whole point of the lane.
		args = append(args, "-")
	} else {
		args = append(args, filepath.ToSlash(archive))
	}
	for _, input := range inputs {
		args = append(args, filepath.ToSlash(input))
	}
	return args, nil
}

// WritesToStandardOutput reports whether the Info-ZIP invocation for this
// fixture streams the archive rather than writing it to a path.
func (c ArchiveCase) WritesToStandardOutput() bool {
	return c.ArchiveFormat == Zip && c.ZipStructure == StreamedZipStructure
}

// validateInfoZipStructure refuses the structures the pinned Info-ZIP writer
// cannot produce for this lane.
func (c ArchiveCase) validateInfoZipStructure() error {
	switch c.ZipStructure {
	case ClassicZipStructure, ForcedZip64Structure, StreamedZipStructure:
		return nil
	case LargeZip64Structure:
		return fmt.Errorf("fixture %q asks Info-ZIP for the %q structure: the member past 4 GiB is written by the pinned 7-Zip, which switches to zip64 there on its own", c.ID, c.ZipStructure)
	default:
		return fmt.Errorf("fixture %q has unsupported zip_structure %q", c.ID, c.ZipStructure)
	}
}

// SevenZipPasswordArgs returns the password selector every read-side 7-Zip
// invocation needs for an encrypted fixture. 7-Zip prompts interactively
// without it, which would hang a generation run.
func (c ArchiveCase) SevenZipPasswordArgs() []string {
	if !c.RequiresPassword() {
		return nil
	}
	return []string{"-p" + FixturePassword}
}

func (c ArchiveCase) RequiresPassword() bool {
	return c.Encryption != NoEncryption
}

// PostEncodingOrDefault is what the fixture's articles are encoded with.
// Manifests written before the uuencode lane existed carry no encoding at
// all, and every one of them is yEnc.
func (c ArchiveCase) PostEncodingOrDefault() PostEncoding {
	if c.Encoding == "" {
		return YEncEncoding
	}
	return c.Encoding
}
