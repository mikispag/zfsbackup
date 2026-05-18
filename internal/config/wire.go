// Package config — wire.go defines the internal JSON protocol types exchanged
// between the sender and receiver over the SSH connection.
//
// The wire protocol is a simple request/response pattern on the SSH channel:
//
//   - For incremental_suggestions: the sender sends no body; the receiver
//     writes a JSON-encoded IncrementalSuggestions to stdout.
//
//   - For set_placeholders: the sender writes a JSON-encoded SetPlaceholders
//     to the receiver's stdin; the receiver processes it and exits.
//
// Both sides should be running the same version of zfsbackup. The wire
// types use json.Unmarshal (not DisallowUnknownFields), so unknown fields
// are silently ignored — the protocol is forward-compatible but there is no
// automatic detection of a version mismatch.
package config

// IncrementalSuggestions is the JSON response written by the receiver to
// stdout in response to an --op=incremental_suggestions request. It tells
// the sender what to transmit next.
//
// Exactly one of the following scenarios applies:
//   - SendFull is true: the destination has no recoverable state; the sender
//     must transmit a full (non-incremental) stream.
//   - ResumeToken is non-empty: an incomplete receive is in progress. Pass
//     this value to zfs send -t to resume instead of restarting.
//   - LastSnapshot and GUID are non-empty: continue incrementally from the
//     identified snapshot.
//   - All fields are zero: the destination dataset exists but has no common
//     base with the source; the sender skips this filesystem unless
//     ForceOverwrite is set, in which case it transmits a full stream and
//     the receiver applies zfs receive -F.
type IncrementalSuggestions struct {
	// SendFull, when true, instructs the sender to transmit a complete
	// (non-incremental) stream. Mutually exclusive with ResumeToken.
	SendFull bool `json:"send_full,omitempty"`

	// ResumeToken is the value of the receive_resume_token ZFS property on the
	// destination. Pass it to zfs send -t to resume the interrupted transfer.
	ResumeToken string `json:"resume_token,omitempty"`

	// LastSnapshot is the name of the most recent snapshot on the destination,
	// without the dataset prefix (e.g. "snap-2024-01-01_00-00-00"). Used
	// together with GUID to locate the incremental base on the sender side.
	LastSnapshot string `json:"last_snapshot,omitempty"`

	// GUID is the GUID of LastSnapshot. Matched against the sender's local
	// snapshot and bookmark GUIDs; GUID matching is required because a snapshot
	// may have been renamed or promoted since it was sent.
	GUID string `json:"guid,omitempty"`

	// ForceOverwrite signals that the destination is listed in the receiver's
	// force_overwrite_datasets. When set, the sender transmits a full stream
	// even if no common base exists, and the receiver applies zfs receive -F.
	ForceOverwrite bool `json:"force_overwrite,omitempty"`
}

// SetPlaceholders is the JSON payload the sender writes to the receiver's
// stdin during a --op=set_placeholders operation. It instructs the receiver
// to create placeholder bookmarks on the destination that mirror those on the
// source, enabling multiple backup destinations to share a common incremental
// base.
type SetPlaceholders struct {
	// FS is the source filesystem name (e.g. "mypool/data"), used to locate
	// the destination dataset under base_dataset and to verify sender/receiver
	// agreement on the target dataset.
	FS string `json:"fs"`

	// Placeholders lists the bookmarks to create on the destination.
	Placeholders []PlaceholderEntry `json:"placeholders,omitempty"`
}

// PlaceholderEntry describes a single placeholder bookmark to create.
type PlaceholderEntry struct {
	// Name is the bookmark suffix (e.g. "mybackup"). The receiver creates a
	// bookmark named #<snapname>-<name> on the destination snapshot identified
	// by GUID.
	Name string `json:"name"`

	// GUID is the GUID of the source snapshot this placeholder points to. The
	// receiver matches this against destination snapshot and bookmark GUIDs to
	// find the correct anchor point.
	GUID string `json:"guid"`
}
