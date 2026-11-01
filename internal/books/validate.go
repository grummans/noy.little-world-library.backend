package books

// Input validation and slug generation; no database or storage access.

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

	switch meta.AccessType {
	case AccessFree:
		meta.PriceVND = nil
	case AccessPaid:
		if meta.PriceVND == nil || *meta.PriceVND <= 0 {
			return Metadata{}, fmt.Errorf("%w: paid book needs a price", ErrValidation)
		}
	default:
		return Metadata{}, fmt.Errorf("%w: access type must be free or paid", ErrValidation)
	}

	if err := validateAges(meta.AgeMin, meta.AgeMax); err != nil {
		return Metadata{}, err
	}
	return meta, nil
}

func validateAges(min, max *int16) error {
	for _, age := range []*int16{min, max} {
		if age != nil && (*age < 0 || *age > maxAge) {
			return fmt.Errorf("%w: age must be between 0 and %d", ErrValidation, maxAge)
		}
	}
	if min != nil && max != nil && *min > *max {
		return fmt.Errorf("%w: minimum age is above maximum age", ErrValidation)
	}
	return nil
}
