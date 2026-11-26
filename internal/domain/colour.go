package domain

// The entity palette.
//
// Entities store a palette KEY - "blue", "slate" - and never a colour value.
// The stylesheet maps each key to a value chosen for the theme in use, so an
// assignment that is blue stays legible on a light background, a dark one and
// the terminal theme's phosphor green, with one definition per theme rather
// than one per entity. See docs/adr/0011-theming-via-css-custom-properties.md.
//
// The list lives here rather than in the web layer because it is now the
// vocabulary of two different things: the colour an entity carries, and the
// colour the weekend's day numbers are drawn in. Two copies of a list like this
// drift the first time an eighth colour is added, and the drift is silent - a
// key one copy knows about renders as no colour at all through the other.

// ColourKeys are the palette entries anything in this application may be drawn
// in, in the order a picker should offer them.
var ColourKeys = []string{"blue", "green", "amber", "red", "purple", "teal", "slate"}

// DefaultColourKey is what an entity with no colour of its own gets: the
// neutral one, so an unclassified thing does not claim a meaning it has not
// earned.
const DefaultColourKey = "slate"

// IsColourKey reports whether a string names a palette entry.
func IsColourKey(key string) bool {
	for _, known := range ColourKeys {
		if known == key {
			return true
		}
	}
	return false
}

// ColourKeyOr returns key if it names a palette entry, and fallback otherwise.
//
// Degrading rather than refusing, which is how every other presentation choice
// in the settings behaves: an unrecognised colour is not worth making somebody
// re-enter their currency over. The important half is that an unknown value
// never reaches a template, where it would render as an attribute matching no
// rule and therefore as no colour at all.
func ColourKeyOr(key, fallback string) string {
	if IsColourKey(key) {
		return key
	}
	return fallback
}
