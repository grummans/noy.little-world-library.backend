package bundles

import (
	"fmt"
	"strings"

	"littleworld/internal/slug"
)

func normalize(meta Metadata) (Metadata, error) {
	meta.Title = strings.TrimSpace(meta.Title)
	meta.Description = strings.TrimSpace(meta.Description)
	meta.Slug = strings.TrimSpace(meta.Slug)

	if meta.Title == "" || len(meta.Title) > maxTitleLen {
		return Metadata{}, fmt.Errorf("%w: title is required", ErrValidation)
	}
	if len(meta.Description) > maxDescriptionLen {
		return Metadata{}, fmt.Errorf("%w: description is too long", ErrValidation)
	}

	if meta.Slug == "" {
		meta.Slug = slug.Make(meta.Title)
	}
	if !slug.Valid(meta.Slug) {
		return Metadata{}, fmt.Errorf("%w: slug must be lowercase words separated by dashes", ErrValidation)
	}

	// Bundles are always paid products, per the product spec.
	if meta.PriceVND == nil || *meta.PriceVND <= 0 {
		return Metadata{}, fmt.Errorf("%w: bundle price must be greater than zero", ErrValidation)
	}
	return meta, nil
}

func validateBookIDs(ids []int64) ([]int64, error) {
	if len(ids) > maxBooks {
		return nil, fmt.Errorf("%w: too many books", ErrValidation)
	}

	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: invalid book id", ErrValidation)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique, nil
}
