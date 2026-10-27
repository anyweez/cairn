// A module in the state someone leaves it in right after adding an import and
// before running `go mod tidy`: the go directive is below the cairn module's,
// so the transitive requirements this module imports have to be listed here
// and are not, and `go list ./...` refuses to load anything.
module example.com/unloadable

go 1.21

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => ../../../..
