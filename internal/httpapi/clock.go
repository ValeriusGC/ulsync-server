package httpapi

import "time"

// serverNowMs is the store's Unix time in milliseconds, the same unit as
// envelope last_edited_at_ms. Clients use it to correct a skewed device
// clock. It is not last-write-wins: the upsert still compares the client's
// last_edited_at_ms. HTTP Date is seconds and may be rewritten by a proxy.
func serverNowMs() int64 { return time.Now().UTC().UnixMilli() }
