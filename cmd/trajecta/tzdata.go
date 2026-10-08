package main

// Same reasoning as cmd/server: the CLI runs from the same bare alpine image and
// reports timestamps in the configured zone, so it needs a zone database even
// when the image does not ship one.
import _ "time/tzdata"
