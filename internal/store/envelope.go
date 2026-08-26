package store

// Envelope is one client-supplied record ready for persistence. It mirrors the
// wire fields from protocol/SPEC.md §1.1 except that Payload holds decoded
// bytes: the HTTP layer strips base64 and the store treats those bytes as
// opaque (SPEC §8).
type Envelope struct {
	// ID identifies the record; stable across devices for a given user.
	ID string
	// Part names which slice of the record this row holds; identity is (id, part).
	Part string
	// EntityType is a codec hint for the receiving client; the server stores it
	// verbatim and keeps no registry of allowed values.
	EntityType string
	// SourceID is the producing installation; third tie-breaker in last-write-wins.
	SourceID string
	// PayloadEncoding is copied as sent (for example "json"); not interpreted here.
	PayloadEncoding string

	// CreatedAtMS is milliseconds since Unix epoch; never updated after insert.
	CreatedAtMS int64
	// LastEditedAtMS is the first rank of conflict resolution.
	LastEditedAtMS int64

	// Revision is the edit counter; second rank of conflict resolution.
	Revision int64
	// Flags carries protocol bits; round 1 stores them without interpreting deleted.
	Flags int64
	// SchemaVersion is the payload format version on the producing client.
	SchemaVersion int64

	// Payload is opaque bytes after base64 decoding; not validated as JSON or UTF-8.
	Payload []byte
}
