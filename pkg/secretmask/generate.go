package secretmask

// The credential DFA in rules_ragel.go is checked in, so a normal build never
// needs Ragel. Regenerate it only when rules.rl changes, and always commit the
// regenerated file together with the .rl change.
//
// ragel-go is required and is not the same binary as ragel: Ragel 7.x ships one
// host-language binary per language, and the generic ragel binary emits C for
// this machine.
//
//go:generate ragel-go -G2 -o rules_ragel.go rules.rl
//go:generate gofmt -w rules_ragel.go
//
// The access indicators (URLs and local paths) are a second machine. They are
// ReportOnly and must not share the credential scanner: a Ragel scanner consumes
// what it matched, so a URL rule there would swallow a credential embedded in
// the URL and the masker would never see it.
//
//go:generate ragel-go -G2 -o indicator_ragel.go indicator.rl
//go:generate gofmt -w indicator_ragel.go
