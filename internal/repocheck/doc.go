// Package repocheck holds the tests that are about the repository rather than
// about any one package.
//
// Most of what this application promises is provable by running code: a
// rounding rule either produces the right minor units or it does not. A few
// promises are not like that. "No cgo", "no floating point in a persisted
// field", "nothing ever updates an audit row" and "every aggregation excludes
// unaccepted proxy time" are all statements about code that was *not* written,
// and the failure mode is silent - a new file, a new column, a new query, and
// the guarantee is quietly gone with every existing test still green.
//
// So these read the tree. They parse the Go source and the migrations and fail
// on the shape of what is there, which is the only way a test can notice an
// omission. Each one names the requirement it stands for, because a source scan
// with no stated reason is the first thing somebody deletes when it fails.
//
// The package deliberately contains no code of its own. It exists as a place
// where a rule that spans packages can live without one package claiming
// ownership of it.
package repocheck
