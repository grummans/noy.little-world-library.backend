package storage

// EPUB facts are read from the package document inside the ZIP: the spine gives the page
// count for fixed-layout books (Book Creator exports those), and the manifest tells us
// whether the book embeds audio or video.
//
// The package document is attacker-controlled input, so its decompressed size is capped
// and every path is validated before use.

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	// A package document is a few hundred KB at most; anything larger is an attack or a
	// broken file, and decoding it would cost memory we do not need.
	maxPackageBytes = 8 << 20
	maxSpineItems   = 5000
)

var errPackageTooLarge = errors.New("epub package document is too large")

// Facts describes what a book contains, derived from its EPUB.
type Facts struct {
	PageCount int // spine items; fixed-layout: one item is one page
	HasAudio  bool
	HasVideo  bool
}

type containerXML struct {
	Rootfiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type packageXML struct {
	Manifest []struct {
		ID        string `xml:"id,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef  string `xml:"idref,attr"`
		Linear string `xml:"linear,attr"`
	} `xml:"spine>itemref"`
}

// InspectEPUB reads the EPUB package and returns its page count and media flags. An EPUB
// without a usable package document is rejected: a book that cannot be inspected must not
// reach the catalog, otherwise the UI would have to guess its size.
func InspectEPUB(r io.ReaderAt, size int64) (Facts, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return Facts{}, ErrInvalidEpub
	}

	rootfiles, err := opfCandidates(zr)
	if err != nil {
		return Facts{}, err
	}

	// The first rootfile that actually exists and parses wins: some generators list a
	// fallback rendition after the primary one.
	for _, candidate := range rootfiles {
		entry, err := findEntry(zr, candidate)
		if err != nil {
			continue
		}
		opf, err := readPackage(entry)
		if err != nil {
			continue
		}
		facts, err := factsFromPackage(opf)
		if err != nil {
			continue
		}
		return facts, nil
	}
	return Facts{}, fmt.Errorf("%w: no usable package document", ErrInvalidEpub)
}

// opfCandidates lists package documents in preference order: the OEBPS package media type
// first, then whatever else the container mentions.
func opfCandidates(zr *zip.Reader) ([]string, error) {
	container, err := findEntry(zr, "META-INF/container.xml")
	if err != nil {
		return nil, fmt.Errorf("%w: missing container.xml", ErrInvalidEpub)
	}

	var parsed containerXML
	if err := decodeXMLFile(container, &parsed); err != nil {
		return nil, fmt.Errorf("%w: unreadable container.xml", ErrInvalidEpub)
	}

	var preferred, rest []string
	for _, rootfile := range parsed.Rootfiles {
		if !safeZipPath(rootfile.FullPath) {
			continue
		}
		if strings.EqualFold(rootfile.MediaType, "application/oebps-package+xml") {
			preferred = append(preferred, rootfile.FullPath)
			continue
		}
		rest = append(rest, rootfile.FullPath)
	}

	candidates := append(preferred, rest...)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: container.xml lists no usable package", ErrInvalidEpub)
	}
	return candidates, nil
}

func readPackage(file *zip.File) (packageXML, error) {
	var parsed packageXML
	// UncompressedSize64 is declared by the ZIP; refuse before decompressing.
	if file.UncompressedSize64 > maxPackageBytes {
		return parsed, errPackageTooLarge
	}

	rc, err := file.Open()
	if err != nil {
		return parsed, err
	}
	defer rc.Close()

	// The declared size can lie, so the reader is capped too.
	if err := decodeXML(io.LimitReader(rc, maxPackageBytes), &parsed); err != nil {
		return packageXML{}, err
	}
	if len(parsed.Spine) > maxSpineItems {
		return packageXML{}, fmt.Errorf("%w: too many spine items", ErrInvalidEpub)
	}
	return parsed, nil
}

// factsFromPackage counts spine items (fixed-layout: one item is one page). An itemref
// that does not resolve to a manifest entry is skipped rather than counted, so a malformed
// book cannot inflate its page count.
func factsFromPackage(opf packageXML) (Facts, error) {
	if len(opf.Spine) == 0 {
		return Facts{}, fmt.Errorf("%w: empty spine", ErrInvalidEpub)
	}

	mediaTypes := make(map[string]string, len(opf.Manifest))
	for _, item := range opf.Manifest {
		mediaTypes[item.ID] = strings.ToLower(item.MediaType)
	}

	facts := Facts{}
	counted := map[string]bool{}
	for _, item := range opf.Spine {
		if item.IDRef == "" || counted[item.IDRef] {
			continue
		}
		if _, known := mediaTypes[item.IDRef]; !known {
			continue
		}
		if strings.EqualFold(item.Linear, "no") {
			continue
		}

		counted[item.IDRef] = true
		facts.PageCount++
	}

	if facts.PageCount == 0 {
		return Facts{}, fmt.Errorf("%w: spine references no readable items", ErrInvalidEpub)
	}

	// Audio and video often live in the manifest without being spine items at all.
	for _, mediaType := range mediaTypes {
		switch {
		case strings.HasPrefix(mediaType, "audio/"):
			facts.HasAudio = true
		case strings.HasPrefix(mediaType, "video/"):
			facts.HasVideo = true
		}
	}
	return facts, nil
}

// safeZipPath rejects absolute and escaping paths; a ZIP is not a filesystem, but a
// package path outside the archive root is still malformed.
func safeZipPath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return false
	}
	cleaned := path.Clean(name)
	return cleaned != "." && !strings.HasPrefix(cleaned, "../")
}

func findEntry(zr *zip.Reader, name string) (*zip.File, error) {
	cleaned := path.Clean(strings.TrimPrefix(name, "/"))
	for _, file := range zr.File {
		if path.Clean(file.Name) == cleaned {
			return file, nil
		}
	}
	return nil, zip.ErrFormat
}

func decodeXML(r io.Reader, target any) error {
	decoder := xml.NewDecoder(r)
	// Namespaces vary between EPUB generators; the element names are what matter.
	decoder.Strict = false
	return decoder.Decode(target)
}

func decodeXMLFile(file *zip.File, target any) error {
	if file.UncompressedSize64 > maxPackageBytes {
		return errPackageTooLarge
	}

	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return decodeXML(io.LimitReader(rc, maxPackageBytes), target)
}
