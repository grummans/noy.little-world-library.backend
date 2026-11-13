package activities

// Input validation for teacher game metadata: no database or storage access.

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"littleworld/internal/slug"
)

// Wordwall serves every embed from this host and keeps the game id in the path, so a
// saved link can never point the iframe at another origin or at a non-embed page.
const (
	wordwallHost = "wordwall.net"
	embedPath    = `^/embed/[A-Za-z0-9_-]{8,64}$`
	// Only these display parameters are kept; anything else is dropped so a link
	// cannot smuggle extra instructions to the player.
	embedQueryKeys = "themeId templateId fontStackId"
)

var embedPathPattern = regexp.MustCompile(embedPath)

func normalize(meta Metadata) (Metadata, error) {
	meta.Title = strings.TrimSpace(meta.Title)
	meta.Description = strings.TrimSpace(meta.Description)
	meta.GameType = strings.TrimSpace(meta.GameType)
	meta.Topic = strings.TrimSpace(meta.Topic)
	meta.Slug = strings.TrimSpace(meta.Slug)

	if meta.Title == "" || len([]rune(meta.Title)) > maxTitleLen {
		return Metadata{}, fmt.Errorf("%w: title is required and at most %d characters", ErrValidation, maxTitleLen)
	}
	if len([]rune(meta.Description)) > maxDescriptionLen {
		return Metadata{}, fmt.Errorf("%w: description is too long", ErrValidation)
	}
	if meta.GameType == "" || len([]rune(meta.GameType)) > maxGameTypeLen {
		return Metadata{}, fmt.Errorf("%w: game type is required", ErrValidation)
	}
	if len([]rune(meta.Topic)) > maxTopicLen {
		return Metadata{}, fmt.Errorf("%w: topic is too long", ErrValidation)
	}

	embedURL, err := normalizeEmbedURL(meta.EmbedURL)
	if err != nil {
		return Metadata{}, err
	}
	meta.EmbedURL = embedURL

	skills, err := normalizeSkills(meta.Skills)
	if err != nil {
		return Metadata{}, err
	}
	meta.Skills = skills

	if meta.Slug == "" {
		meta.Slug = slug.Make(meta.Title)
	}
	if !slug.Valid(meta.Slug) {
		return Metadata{}, fmt.Errorf("%w: slug must be lowercase words separated by dashes", ErrValidation)
	}

	if err := validateAges(meta.AgeMin, meta.AgeMax); err != nil {
		return Metadata{}, err
	}
	return meta, nil
}

// normalizeEmbedURL rebuilds the URL from the parts it accepts, so the stored value is
// canonical and can be put straight into an iframe src.
func normalizeEmbedURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: embed URL is not a valid URL", ErrValidation)
	}
	if parsed.Scheme != "https" || parsed.Host != wordwallHost || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: embed URL must start with https://%s", ErrValidation, wordwallHost)
	}
	if !embedPathPattern.MatchString(parsed.Path) {
		return "", fmt.Errorf("%w: embed URL must be a Wordwall /embed/ link", ErrValidation)
	}

	allowed := map[string]bool{}
	for _, key := range strings.Fields(embedQueryKeys) {
		allowed[key] = true
	}
	query := url.Values{}
	for key, values := range parsed.Query() {
		if !allowed[key] {
			continue
		}
		value := values[len(values)-1]
		if _, err := strconv.Atoi(value); err != nil {
			return "", fmt.Errorf("%w: embed URL parameter %s must be a number", ErrValidation, key)
		}
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func normalizeSkills(skills []string) ([]string, error) {
	cleaned := make([]string, 0, len(skills))
	seen := map[string]bool{}
	for _, skill := range skills {
		skill = strings.TrimSpace(skill)
		if skill == "" {
			continue
		}
		if len([]rune(skill)) > maxSkillLen {
			return nil, fmt.Errorf("%w: each skill must be at most %d characters", ErrValidation, maxSkillLen)
		}
		key := strings.ToLower(skill)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, skill)
	}
	if len(cleaned) > maxSkills {
		return nil, fmt.Errorf("%w: at most %d skills", ErrValidation, maxSkills)
	}
	return cleaned, nil
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

func normalizeStatus(status string) (string, error) {
	switch status {
	case StatusDraft, StatusPublished, StatusHidden:
		return status, nil
	default:
		return "", fmt.Errorf("%w: status must be draft, hidden or published", ErrValidation)
	}
}
