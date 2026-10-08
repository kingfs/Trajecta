package main

// The container runtime is a bare alpine image that has no /usr/share/zoneinfo
// unless the tzdata package is installed, and Go reports a missing zone as a
// plain lookup error. Every display-timezone setting lives behind
// time.LoadLocation, so without a zone database the configured zone would fail
// to load and the Monitor would quietly fall back to UTC - the exact bug that
// setting exists to fix. The Dockerfile installs tzdata as well; embedding the
// IANA database in the binary keeps the setting working even on an image that
// does not, at the cost of a few hundred kilobytes.
import _ "time/tzdata"
