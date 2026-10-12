module github.com/creachadair/chirp

go 1.27

require (
	github.com/creachadair/command v0.2.12
	github.com/creachadair/flax v0.1.0
	github.com/creachadair/mds v0.31.0
	github.com/creachadair/taskgroup v0.14.4
	github.com/google/go-cmp v0.7.0
)

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	honnef.co/go/tools v0.8.2 // indirect
)

tool honnef.co/go/tools/staticcheck

retract v0.4.8 // incorrect shutdown semantics in PipeChannel
