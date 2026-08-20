module github.com/creachadair/chirp

go 1.26.0

require (
	github.com/creachadair/taskgroup v0.14.4
	github.com/google/go-cmp v0.7.0
)

require (
	github.com/creachadair/command v0.2.11
	github.com/creachadair/flax v0.0.6
	github.com/creachadair/mds v0.30.5
)

require (
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/tools v0.44.1-0.20260420230617-19499e7caabc // indirect
	honnef.co/go/tools v0.8.0 // indirect
)

tool honnef.co/go/tools/staticcheck

retract v0.4.8 // incorrect shutdown semantics in PipeChannel
