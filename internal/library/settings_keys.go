package library

const (
	// SettingKeyAutoQueueNewBooks controls whether sync runs automatically queue
	// books that end up in "new" status.
	SettingKeyAutoQueueNewBooks = "sync_auto_queue_new"

	// SettingKeyCoverageBasis controls whether coverage percentages use all
	// library items or only available items (all minus unavailable).
	SettingKeyCoverageBasis = "coverage_basis"

	CoverageBasisAll       = "all"
	CoverageBasisAvailable = "available"
)
