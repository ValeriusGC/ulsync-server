package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// metaKeyOrigin is the server_meta row that records which application contour
// owns this store. The key name is fixed in code so SQL never embeds a magic
// string at insert sites.
const metaKeyOrigin = "origin"

// ErrOriginRequired means the request must carry a well-formed Ulsync-Origin
// header before sync mail may proceed. The HTTP layer maps this to 400 with
// {"error":"origin_required"}.
var ErrOriginRequired = errors.New("origin_required")

// OriginMismatchError means the client named a different application contour
// than the store already holds. The HTTP layer maps this to 409 with the
// fixture fields store_origin and request_origin.
type OriginMismatchError struct {
	StoreOrigin   string
	RequestOrigin string
}

func (e *OriginMismatchError) Error() string {
	return fmt.Sprintf(
		"origin_mismatch: store=%q request=%q",
		e.StoreOrigin,
		e.RequestOrigin,
	)
}

// Origin reports the store origin recorded in server_meta, or "" when the
// store is still open and unimprinted.
//
// Reads use the reader pool: comparison on existing requests does not need the
// writer lock and must not contend with imprint writes on hello.
func (s *Store) Origin(ctx context.Context) (string, error) {
	return s.readMeta(ctx, metaKeyOrigin)
}

// BindAuthoredOrigin pins an authored store at process start when origin is set
// in configuration. It is not called from the HTTP path; EnsureOrigin handles
// per-request checks once the store is named.
//
// An empty authored value is a no-op (open store). When authored is non-empty
// and server_meta has no origin yet, authored is written through the writer
// pool. When stored and authored differ, the process must not start: mounting a
// foreign database volume under this configuration would silently serve the
// wrong contour.
func (s *Store) BindAuthoredOrigin(ctx context.Context, authored string) error {
	if authored == "" {
		return nil
	}
	stored, err := s.Origin(ctx)
	if err != nil {
		return err
	}
	if stored == "" {
		return s.writeMeta(ctx, metaKeyOrigin, authored)
	}
	if stored != authored {
		return fmt.Errorf(
			"authored origin %q does not match stored origin %q in server_meta",
			authored,
			stored,
		)
	}
	return nil
}

// EnsureOrigin applies SPEC §1.5 / §3.5 for one authenticated request.
//
// authored is the configuration value; empty means an open store. request is
// the Ulsync-Origin header after HTTP validation; empty means a legacy client
// or a missing hello header. imprint is true only for GET /v1/sync/hello: that
// is the only path allowed to write the origin of an open store. Mail endpoints
// pass imprint=false so push cannot win a race against hello.
//
// It returns the store origin after the call, or an error whose type the HTTP
// layer maps to 400 / 409. Hello and the sync middleware call the same
// function so two copies cannot drift.
func (s *Store) EnsureOrigin(ctx context.Context, authored, request string, imprint bool) (string, error) {
	stored, err := s.Origin(ctx)
	if err != nil {
		return "", err
	}

	// Authored store: configuration already named the contour at startup.
	if authored != "" {
		switch {
		case request == "":
			return "", ErrOriginRequired
		case request != authored:
			return "", &OriginMismatchError{StoreOrigin: authored, RequestOrigin: request}
		default:
			return authored, nil
		}
	}

	// Open store.
	if stored == "" {
		switch {
		case request == "" && !imprint:
			return "", nil
		case request == "" && imprint:
			return "", ErrOriginRequired
		case request != "" && imprint:
			if err := s.writeMeta(ctx, metaKeyOrigin, request); err != nil {
				return "", err
			}
			return request, nil
		case request != "" && !imprint:
			return "", ErrOriginRequired
		default:
			return "", nil
		}
	}

	// Open store already imprinted.
	switch {
	case request == "" && !imprint:
		return stored, nil
	case request == "" && imprint:
		return "", ErrOriginRequired
	case request == stored:
		return stored, nil
	default:
		return "", &OriginMismatchError{StoreOrigin: stored, RequestOrigin: request}
	}
}

// readMeta loads one server_meta value through the reader pool.
func (s *Store) readMeta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.readDB.QueryRowContext(ctx, `
		SELECT v FROM server_meta WHERE k = ?
	`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("read server_meta %q: %w", key, err)
	}
	return value, nil
}

// writeMeta upserts one server_meta row through the writer pool. Origin
// imprinting and authored bind both mutate metadata, never envelopes.
func (s *Store) writeMeta(ctx context.Context, key, value string) error {
	_, err := s.writeDB.ExecContext(ctx, `
		INSERT INTO server_meta (k, v) VALUES (?, ?)
		ON CONFLICT (k) DO UPDATE SET v = excluded.v
	`, key, value)
	if err != nil {
		return fmt.Errorf("write server_meta %q: %w", key, err)
	}
	return nil
}
