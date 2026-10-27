// A module whose directory holds no packages at all. `go list ./...` succeeds
// and reports nothing, which is the same empty manifest as a failed load.
module example.com/nopackages

go 1.26.0
