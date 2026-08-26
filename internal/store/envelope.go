package store

// Envelope is one client-supplied record. Payload holds decoded bytes after
// the HTTP layer strips base64; the store treats those bytes as opaque.
type Envelope struct {
	ID, Part, EntityType, SourceID, PayloadEncoding string
	CreatedAtMS, LastEditedAtMS                     int64
	Revision, Flags, SchemaVersion                  int64
	Payload                                         []byte
}
