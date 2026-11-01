package storagetest

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// SpineRef is one <itemref> in the package document.
type SpineRef struct {
	IDRef  string
	Linear string
}

// EPUBOptions lets tests build files that are EPUB-shaped but invalid in one way.
type EPUBOptions struct {
	// DeflateMimetype compresses the mimetype entry (the spec requires Store).
	DeflateMimetype bool
	// SkipContainer drops META-INF/container.xml.
	SkipContainer bool
	// Mimetype overrides the mimetype payload.
	Mimetype string
	// MimetypeLast puts the mimetype entry after other entries.
	MimetypeLast bool

	// SpineRefs replaces the default spine; page count comes from this list.
	SpineRefs []SpineRef
	// SpineHasEmptyRef adds an <itemref> without idref.
	SpineHasEmptyRef bool
	// SpineRefsUnknown points every itemref at a manifest entry that does not exist.
	SpineRefsUnknown bool
	// EmptySpine emits <spine></spine>.
	EmptySpine bool
	// PackagePadding bloats the package document, for the size-limit test.
	PackagePadding int
}

// defaultSpine is three pages, one of them a video, plus audio in the manifest.
var defaultSpine = []SpineRef{{IDRef: "page1"}, {IDRef: "page2"}, {IDRef: "clip"}}

// EPUB builds a spec-shaped EPUB: mimetype stored first, then container.xml and a page.
func EPUB(t testing.TB, opts ...func(*EPUBOptions)) []byte {
	t.Helper()

	cfg := EPUBOptions{Mimetype: "application/epub+zip"}
	for _, opt := range opts {
		opt(&cfg)
	}

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	writeMimetype := func() {
		header := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
		if cfg.DeflateMimetype {
			header.Method = zip.Deflate
		}
		entry, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("create mimetype: %v", err)
		}
		if _, err := entry.Write([]byte(cfg.Mimetype)); err != nil {
			t.Fatalf("write mimetype: %v", err)
		}
	}
	if !cfg.MimetypeLast {
		writeMimetype()
	}

	if !cfg.SkipContainer {
		entry, err := zw.Create("META-INF/container.xml")
		if err != nil {
			t.Fatalf("create container: %v", err)
		}
		if _, err := entry.Write([]byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)); err != nil {
			t.Fatalf("write container: %v", err)
		}
	}

	// The package document drives page count and media detection.
	// The package document drives page count and media detection.
	spine := cfg.SpineRefs
	if len(spine) == 0 && !cfg.EmptySpine {
		spine = defaultSpine
	}

	var packageXML strings.Builder
	packageXML.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <manifest>
    <item id="page1" href="page.xhtml" media-type="application/xhtml+xml"/>
    <item id="page2" href="page2.xhtml" media-type="application/xhtml+xml"/>
    <item id="page3" href="page3.xhtml" media-type="application/xhtml+xml"/>
    <item id="clip" href="media/clip.mp4" media-type="video/mp4"/>
    <item id="sound" href="media/sound.mp3" media-type="audio/mpeg"/>
  </manifest>
  <spine>
`)
	for _, ref := range spine {
		idref := ref.IDRef
		if cfg.SpineRefsUnknown {
			idref = "missing-" + idref
		}
		linear := ""
		if ref.Linear != "" {
			linear = ` linear="` + ref.Linear + `"`
		}
		packageXML.WriteString(`    <itemref idref="` + idref + `"` + linear + " />\n")
	}
	if cfg.SpineHasEmptyRef {
		packageXML.WriteString("    <itemref />\n")
	}
	packageXML.WriteString(`  </spine>
`)
	if cfg.PackagePadding > 0 {
		packageXML.WriteString("  <!--" + strings.Repeat("x", cfg.PackagePadding) + "-->\n")
	}
	packageXML.WriteString("</package>")

	opf, err := zw.Create("EPUB/package.opf")
	if err != nil {
		t.Fatalf("create package.opf: %v", err)
	}
	if _, err := opf.Write([]byte(packageXML.String())); err != nil {
		t.Fatalf("write package.opf: %v", err)
	}

	page, err := zw.Create("EPUB/page.xhtml")
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	if _, err := page.Write([]byte("<html><body>hi</body></html>")); err != nil {
		t.Fatalf("write page: %v", err)
	}

	if cfg.MimetypeLast {
		writeMimetype()
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// PNG builds a square PNG of the given edge length.
func PNG(t testing.TB, edge int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, edge, edge))
	for x := 0; x < edge; x++ {
		for y := 0; y < edge; y++ {
			img.Set(x, y, color.RGBA{R: 30, G: 110, B: 180, A: 255})
		}
	}

	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
