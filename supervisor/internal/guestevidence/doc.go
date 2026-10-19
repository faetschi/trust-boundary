// Package guestevidence defines the artifacts the guest contributes to the EVQ results and the
// strict validators `tbound-run evq` applies to them.
//
// An artifact is one JSON document in the envelope
//
//	{"schema":"tbound-guest-evidence/v1","kind":K,"commit":F,"binary_sha256":D,"data":{...}}
//
// that `tbound-profile guest-evidence <kind>` writes on the guest. commit is the freeze commit the
// tool was built from (the operator names it; evq compares it with run.json), and binary_sha256 is
// the sha256 of the tbound-profile executable that wrote the document, taken by the tool from its
// own executable (evq compares it with the digest of the P1 build). data holds the observed facts
// of the kind; every kind states below the values it must show, and a document that is not exactly
// the schema, that has a duplicate key, an unknown member or trailing data, or whose values do not
// show the required fact is refused. A refused artifact is never "present": the obligation it is
// mapped to stays not passed.
//
// An artifact proves what its producer observed on the machine it ran on. It cannot prove that the
// machine is the pinned guest; the runbook ties each producer to a guest step.
package guestevidence
