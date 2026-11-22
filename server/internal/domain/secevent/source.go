package secevent

// Source is where the agent of a node reads the log lines for security events (M2-31, ADR 0025),
// as reported in its last Hello.
type Source string

// The sources; the values are stored in nodes.security_source.
const (
	// SourceUnknown: not reported (an agent before M2-31, or never connected).
	SourceUnknown Source = ""
	// SourceJournal is the systemd journal: every line carries its sender.
	SourceJournal Source = "journal"
	// SourceFileVerified is the file of the SerMon rsyslog template, with the sender's user ID.
	SourceFileVerified Source = "file_verified"
	// SourceFileUnverified is a classic log file: its events are marked unverified.
	SourceFileUnverified Source = "file_unverified"
	// SourceNone: no journald and no readable log file, so the node sends no security events.
	SourceNone Source = "none"
)
