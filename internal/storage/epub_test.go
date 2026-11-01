package storage_test

import (
	"bytes"
	"errors"
	"testing"

	"littleworld/internal/storage"
	"littleworld/internal/storagetest"
)

func mustInspect(t *testing.T, data []byte) storage.Facts {
	t.Helper()
	facts, err := storage.InspectEPUB(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return facts
}

// The fixture is a fixed-layout EPUB: three spine items, including one video and audio in
// the manifest.
func TestInspectEPUBReadsSpineAndMedia(t *testing.T) {
	facts := mustInspect(t, storagetest.EPUB(t))

	if facts.PageCount != 3 {
		t.Errorf("page count = %d, want 3 spine items", facts.PageCount)
	}
	if !facts.HasAudio || !facts.HasVideo {
		t.Errorf("media flags = audio:%v video:%v, want both", facts.HasAudio, facts.HasVideo)
	}
}

func TestInspectEPUBRejectsUnreadablePackages(t *testing.T) {
	cases := map[string]func(*storagetest.EPUBOptions){
		"missing container": func(o *storagetest.EPUBOptions) { o.SkipContainer = true },
		"empty spine":       func(o *storagetest.EPUBOptions) { o.EmptySpine = true },
		"unknown idref":     func(o *storagetest.EPUBOptions) { o.SpineRefsUnknown = true },
	}

	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			data := storagetest.EPUB(t, opt)
			if _, err := storage.InspectEPUB(bytes.NewReader(data), int64(len(data))); !errors.Is(err, storage.ErrInvalidEpub) {
				t.Errorf("err = %v, want ErrInvalidEpub", err)
			}
		})
	}
}

// linear="no" items are not shown when reading forward, so they are not pages.
func TestInspectEPUBSkipsNonLinearSpineItems(t *testing.T) {
	data := storagetest.EPUB(t, func(o *storagetest.EPUBOptions) {
		o.SpineRefs = []storagetest.SpineRef{
			{IDRef: "page1"},
			{IDRef: "page2", Linear: "no"},
			{IDRef: "page3"},
		}
	})
	if facts := mustInspect(t, data); facts.PageCount != 2 {
		t.Errorf("page count = %d, want 2 linear items", facts.PageCount)
	}
}

// A duplicate idref is one page on screen, however often the spine names it.
func TestInspectEPUBCountsDuplicateSpineRefsOnce(t *testing.T) {
	data := storagetest.EPUB(t, func(o *storagetest.EPUBOptions) {
		o.SpineRefs = []storagetest.SpineRef{
			{IDRef: "page1"},
			{IDRef: "page1"},
		}
	})
	if facts := mustInspect(t, data); facts.PageCount != 1 {
		t.Errorf("page count = %d, want 1 distinct page", facts.PageCount)
	}
}

// An oversized package document is refused before it is decoded.
func TestInspectEPUBRejectsHugePackage(t *testing.T) {
	data := storagetest.EPUB(t, func(o *storagetest.EPUBOptions) {
		o.PackagePadding = 9 << 20
	})
	if _, err := storage.InspectEPUB(bytes.NewReader(data), int64(len(data))); err == nil {
		t.Error("oversized package document was accepted")
	}
}

// An itemref without idref cannot describe a page, so it is ignored, not counted and not
// fatal: real books sometimes carry such junk.
func TestInspectEPUBIgnoresEmptyItemRef(t *testing.T) {
	data := storagetest.EPUB(t, func(o *storagetest.EPUBOptions) { o.SpineHasEmptyRef = true })

	if facts := mustInspect(t, data); facts.PageCount != 3 {
		t.Errorf("page count = %d, want the 3 real pages", facts.PageCount)
	}
}
