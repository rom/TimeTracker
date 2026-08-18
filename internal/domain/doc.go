// Package domain holds the core types of the application and the rules that are
// true about them regardless of how they are stored or displayed.
//
// This package deliberately has no dependencies on the database, on HTTP, or on
// anything else in the tree. That constraint is the point: the rules that matter
// most (how money is added up, how a duration is rounded before it is billed,
// whether an entry is in a valid state) can be read and tested without a fixture,
// a server or a database file.
//
// See docs/adr/0012-layered-package-structure.md for the layering rules, and
// docs/adr/0014-exact-money-and-duration.md for why money is an integer type here
// rather than a float.
package domain
