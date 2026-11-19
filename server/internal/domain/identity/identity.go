// Package identity defines the strongly typed IDs of users and nodes (KONZEPT §19.1).
package identity

import "github.com/google/uuid"

// UserID identifies a user (global identity, member of one or more organizations).
type UserID uuid.UUID

// String returns the canonical UUID representation.
func (id UserID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the ID is unset.
func (id UserID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

// NodeID identifies a monitored node.
type NodeID uuid.UUID

// String returns the canonical UUID representation.
func (id NodeID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the ID is unset.
func (id NodeID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }
