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

// NextColourKey returns the palette entry for the nth thing created.
//
// So that an instance is not monochrome. Customers and projects defaulted to
// the neutral key, which meant a seven-colour palette - contrast-tested in
// eight themes, carried by the chips, the week grid, the month calendar and
// every timeline block - showed exactly one colour until somebody went and set
// each customer by hand. Almost nobody did, so almost every instance was grey,
// and the feature that makes a dense screen readable at a glance was off by
// default.
//
// Cycling by position rather than hashing the name: a hash spreads the palette
// evenly over many customers but says nothing about the first three, and two
// adjacent rows in the same colour is exactly the collision that matters on a
// screen holding four. The caller still overrides it - this is the default,
// not a policy.
func NextColourKey(existing int) string {
	if existing < 0 {
		existing = 0
	}
	return ColourKeys[existing%len(ColourKeys)]
}

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
