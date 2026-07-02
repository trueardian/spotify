// Package firestore provides a Cloud Firestore-backed implementation of
// spotify.TokenStore.
//
// Each user maps to one document in a single collection (DefaultCollection
// unless overridden with WithCollection); the userID is the document ID, so
// lookups are direct reads rather than queries. Firestore is schemaless, so
// unlike the postgres sibling there is no Migrate — the collection appears
// with the first saved token.
package firestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.naturallyfunny.dev/spotify"
)

// DefaultCollection is the collection tokens live in unless WithCollection
// overrides it. It matches the table name used by the postgres sibling.
const DefaultCollection = "spotify_tokens"

// tokenDoc is the stored shape of one user's token document.
type tokenDoc struct {
	RefreshToken string    `firestore:"refresh_token"`
	CreatedAt    time.Time `firestore:"created_at"`
}

// Store implements spotify.TokenStore backed by Cloud Firestore.
type Store struct {
	client     *firestore.Client
	collection string
}

// Option configures a Store at construction.
type Option func(*Store)

// WithCollection stores tokens in the named collection instead of
// DefaultCollection. Use it when one Firestore database hosts several
// environments or apps.
func WithCollection(name string) Option {
	return func(s *Store) { s.collection = name }
}

// New builds a Store over an existing *firestore.Client the consumer already
// owns (and stays responsible for closing).
func New(client *firestore.Client, opts ...Option) *Store {
	s := &Store{client: client, collection: DefaultCollection}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Store) GetRefreshToken(ctx context.Context, userID string) (string, error) {
	ref, err := s.doc(userID)
	if err != nil {
		return "", err
	}
	snap, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", spotify.ErrNotConnected
	}
	if err != nil {
		return "", fmt.Errorf("get refresh token: %w", err)
	}
	var doc tokenDoc
	if err := snap.DataTo(&doc); err != nil {
		return "", fmt.Errorf("get refresh token: decode %q: %w", userID, err)
	}
	return doc.RefreshToken, nil
}

// SaveRefreshToken inserts or replaces the refresh token for userID. Like the
// postgres sibling's upsert, created_at is written once on first save and left
// untouched on replacement, so it keeps meaning "when the user first
// connected". The read-then-write runs in a transaction so concurrent saves
// serialize instead of racing on that first write.
func (s *Store) SaveRefreshToken(ctx context.Context, userID, refreshToken string) error {
	ref, err := s.doc(userID)
	if err != nil {
		return err
	}
	err = s.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		_, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return tx.Set(ref, map[string]any{
				"refresh_token": refreshToken,
				"created_at":    firestore.ServerTimestamp,
			})
		}
		if err != nil {
			return err
		}
		return tx.Update(ref, []firestore.Update{
			{Path: "refresh_token", Value: refreshToken},
		})
	})
	if err != nil {
		return fmt.Errorf("save refresh token: %w", err)
	}
	return nil
}

// doc resolves userID to its document reference, rejecting IDs that cannot be
// Firestore document IDs.
func (s *Store) doc(userID string) (*firestore.DocumentRef, error) {
	if err := validateUserID(userID); err != nil {
		return nil, err
	}
	return s.client.Collection(s.collection).Doc(userID), nil
}

// validateUserID enforces Firestore's document-ID constraints on the opaque
// userID string. The userID is used verbatim as the document ID — no escaping —
// so the rare ID that violates a constraint is rejected loudly here instead of
// corrupting a document path or failing server-side with an opaque error.
func validateUserID(userID string) error {
	switch {
	case userID == "":
		return errors.New("spotify/firestore: user ID is empty")
	case userID == "." || userID == "..":
		return fmt.Errorf("spotify/firestore: user ID %q is a reserved document ID", userID)
	case strings.Contains(userID, "/"):
		return fmt.Errorf("spotify/firestore: user ID %q contains '/', not allowed in a document ID", userID)
	case len(userID) > 1500:
		return fmt.Errorf("spotify/firestore: user ID exceeds Firestore's 1500-byte document ID limit (%d bytes)", len(userID))
	case len(userID) >= 4 && strings.HasPrefix(userID, "__") && strings.HasSuffix(userID, "__"):
		return fmt.Errorf("spotify/firestore: user ID %q matches Firestore's reserved __*__ document ID pattern", userID)
	}
	return nil
}

var _ spotify.TokenStore = (*Store)(nil)
