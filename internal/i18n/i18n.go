// Package i18n provides message catalogues and locale-aware formatting.
//
// The design is deliberately small. A full ICU implementation would bring
// gender, ordinals and complex plural categories; this application needs
// translated strings, correct plurals for the two languages it ships, and
// locale-aware numbers and dates. Anything more would be machinery nobody here
// exercises.
//
// Two rules keep translation honest:
//
//   - A missing translation falls back to English rather than showing a key.
//     A user who sees "day.totals.summed" has been failed twice.
//   - Every string the user can read comes from a catalogue. A hard-coded string
//     in a template is a string that cannot be translated, and the test in
//     catalog_test.go fails the build when the catalogues disagree about which
//     keys exist.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Catalogues are embedded so the binary stays self-contained (ASR-003).
//
//go:embed locales/*.json
var localeFS embed.FS

// DefaultLanguage is the fallback, and the language every key must exist in.
const DefaultLanguage = "en"

// Language describes one supported locale.
type Language struct {
	// Code is the BCP 47 tag, e.g. "en" or "sv".
	Code string
	// Name is the language's name in the language itself, which is what a
	// language picker should show: someone looking for Swedish is looking for
	// "Svenska", not "Swedish".
	Name string
}

// catalogue holds one language's messages.
type catalogue struct {
	code     string
	messages map[string]string
}

var (
	loadOnce   sync.Once
	catalogues map[string]*catalogue
	languages  []Language
	loadErr    error
)

// load reads the embedded catalogues once.
func load() {
	catalogues = map[string]*catalogue{}

	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		loadErr = fmt.Errorf("read embedded locales: %w", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		code := strings.TrimSuffix(entry.Name(), ".json")

		raw, err := localeFS.ReadFile("locales/" + entry.Name())
		if err != nil {
			loadErr = fmt.Errorf("read locale %s: %w", code, err)
			return
		}

		var file struct {
			Name     string            `json:"$name"`
			Messages map[string]string `json:"-"`
		}
		var all map[string]string
		if err := json.Unmarshal(raw, &all); err != nil {
			loadErr = fmt.Errorf("parse locale %s: %w", code, err)
			return
		}
		file.Name = all["$name"]
		delete(all, "$name")
		file.Messages = all

		catalogues[code] = &catalogue{code: code, messages: file.Messages}
		languages = append(languages, Language{Code: code, Name: file.Name})
	}

	if _, ok := catalogues[DefaultLanguage]; !ok {
		loadErr = fmt.Errorf("the default language %q has no catalogue", DefaultLanguage)
		return
	}

	// A stable order, with the default first, so a language picker does not
	// reshuffle between builds.
	sort.Slice(languages, func(i, j int) bool {
		if languages[i].Code == DefaultLanguage {
			return true
		}
		if languages[j].Code == DefaultLanguage {
			return false
		}
		return languages[i].Code < languages[j].Code
	})
}

// Languages returns the supported languages, default first.
func Languages() []Language {
	loadOnce.Do(load)
	return languages
}

// Supported reports whether a language code has a catalogue.
func Supported(code string) bool {
	loadOnce.Do(load)
	_, ok := catalogues[normalise(code)]
	return ok
}

// LoadError reports a problem with the embedded catalogues. It is checked at
// start-up so a malformed catalogue fails the process rather than showing keys
// to a user.
func LoadError() error {
	loadOnce.Do(load)
	return loadErr
}

// Printer renders messages in one language.
//
// It is created per request and carried on the page data, so a template writes
// {{.T "nav.today"}} and gets the right language without any global state.
type Printer struct {
	primary  *catalogue
	fallback *catalogue
	code     string
	// formats overrides the conventions the language would otherwise imply. An
	// organisation whose accounting department wants 12-hour times on a Swedish
	// interface gets to have them; leaving these empty keeps the language's own
	// conventions, which is what every caller did before the setting existed.
	formats Formats
}

// Formats are the presentation choices that override a language's defaults.
//
// Both fields are the string forms of the domain's ClockFormat and DateFormat.
// They are plain strings here rather than the domain types because this package
// sits below the domain and must not import it: it is a leaf, embedded in the
// binary and used by the store's own error messages.
type Formats struct {
	// Clock is "24h", "12h", or empty for the language's own convention.
	Clock string
	// Date is "iso", "dmy", "mdy", or empty for the language's own convention.
	Date string
}

// WithFormats returns a printer that writes clocks and dates as asked.
//
// A copy rather than a mutation: printers are built per request and handed to a
// template, and a shared printer whose formats could change under it would be a
// data race waiting for the first concurrent render.
func (p *Printer) WithFormats(f Formats) *Printer {
	clone := *p
	clone.formats = f
	return &clone
}

// NewPrinter returns a printer for a language code, falling back to the default
// for anything unrecognised.
func NewPrinter(code string) *Printer {
	loadOnce.Do(load)

	fallback := catalogues[DefaultLanguage]
	primary, ok := catalogues[normalise(code)]
	if !ok {
		primary = fallback
	}
	return &Printer{primary: primary, fallback: fallback, code: primary.code}
}

// Code returns the language actually in use, which is what belongs in the
// document's lang attribute - a screen reader uses it to pick a voice, so
// claiming Swedish while rendering English is actively harmful.
func (p *Printer) Code() string { return p.code }

// T translates a key, substituting %s-style arguments.
//
// A key with no translation in the chosen language falls back to English; a key
// missing everywhere returns the key itself, which is ugly on screen and easy to
// spot in review - deliberately, since silently rendering nothing would hide the
// mistake.
func (p *Printer) T(key string, args ...any) string {
	message, ok := p.primary.messages[key]
	if !ok || message == "" {
		message, ok = p.fallback.messages[key]
	}
	if !ok || message == "" {
		return key
	}
	if len(args) == 0 {
		return message
	}
	return fmt.Sprintf(message, args...)
}

// N translates a countable message, choosing the singular or plural form.
//
// The catalogue holds "key.one" and "key.other". English and Swedish share the
// same two-form rule, so this is sufficient for both; a language with more
// categories (Polish, Arabic, Russian) would need real CLDR plural rules, and
// this is the function that would grow them.
func (p *Printer) N(key string, count int, args ...any) string {
	suffix := ".other"
	if count == 1 {
		suffix = ".one"
	}
	all := append([]any{count}, args...)
	return p.T(key+suffix, all...)
}

// normalise reduces a language tag to the part we match on.
//
// "sv-SE" and "sv_SE" both become "sv": this application translates by language,
// not by region, and a Swedish speaker in Finland should still get Swedish.
func normalise(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "_", "-")
	if base, _, ok := strings.Cut(code, "-"); ok {
		return base
	}
	return code
}

// Negotiate picks a language from an Accept-Language header.
//
// It parses the quality values properly rather than taking the first entry:
// browsers commonly send something like "en-GB,en;q=0.9,sv;q=0.8", and a naive
// reading of the first tag would ignore a user whose preferred language is
// listed second with a higher weight.
func Negotiate(header string) string {
	loadOnce.Do(load)
	if header == "" {
		return DefaultLanguage
	}

	type candidate struct {
		code    string
		quality float64
	}
	var candidates []candidate

	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		tag, params, _ := strings.Cut(part, ";")
		quality := 1.0
		if params != "" {
			if _, value, ok := strings.Cut(params, "q="); ok {
				var parsed float64
				if _, err := fmt.Sscanf(strings.TrimSpace(value), "%f", &parsed); err == nil {
					quality = parsed
				}
			}
		}

		code := normalise(tag)
		if code == "*" || !Supported(code) {
			continue
		}
		candidates = append(candidates, candidate{code: code, quality: quality})
	}

	if len(candidates) == 0 {
		return DefaultLanguage
	}
	// A stable sort keeps the header's own order for equal weights, which is
	// what the specification intends.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].quality > candidates[j].quality
	})
	return candidates[0].code
}

// Keys returns every message key in a catalogue, sorted.
//
// It exists for the parity test: a language that quietly lacks a key falls back
// to English in a place nobody noticed, which is how a half-translated interface
// ships.
func Keys(code string) []string {
	loadOnce.Do(load)

	cat, ok := catalogues[normalise(code)]
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(cat.messages))
	for key := range cat.messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Missing reports whether this printer's own language lacks a key, ignoring the
// fallback. Used by the parity test; ordinary rendering should always go through
// T, which falls back rather than showing a key to a user.
func (p *Printer) Missing(key string) bool {
	message, ok := p.primary.messages[key]
	return !ok || message == ""
}
