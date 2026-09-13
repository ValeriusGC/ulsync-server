package store

// Ranks is the ordered part of an envelope that decides conflicts: the three
// ranks of SPEC §2, and nothing else.
type Ranks struct {
	// LastEditedAtMS is the first conflict rank (milliseconds since Unix epoch).
	LastEditedAtMS int64
	// Revision is the second conflict rank (edit counter on the producing client).
	Revision int64
	// SourceID is the third conflict rank (installation identifier).
	SourceID string
}

// Wins reports whether incoming beats stored under the conflict rule of
// SPEC §2: greater LastEditedAtMS, then greater Revision, then greater
// SourceID compared as bytes. A tie on all three is not a win.
//
// String comparison uses Go's > on strings, which orders UTF-8 code units in
// byte order. That matches SQLite TEXT compared with BINARY collation (SPEC §2).
//
// This is the same rule the upsert applies in SQL (upsertEnvelopeSQL). The
// duplication is deliberate and pinned by TestWinsAgreesWithUpsert: the write
// path stays untouched SQL, while a read-only caller needs the rule in Go.
func Wins(incoming, stored Ranks) bool {
	if incoming.LastEditedAtMS != stored.LastEditedAtMS {
		return incoming.LastEditedAtMS > stored.LastEditedAtMS
	}
	if incoming.Revision != stored.Revision {
		return incoming.Revision > stored.Revision
	}
	return incoming.SourceID > stored.SourceID
}
