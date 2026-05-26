package mediaserver

import (
	"context"
	"strings"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
)

func loadBookDestinationItemIDs(ctx context.Context, db database.Database, destinationID string) (map[int64]string, error) {
	rows, err := db.ListBookDestinationsBy(ctx, destinationID, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(rows))
	for _, row := range rows {
		if itemID := strings.TrimSpace(row.ServerItemID); itemID != "" {
			out[row.BookID] = itemID
		}
	}
	return out, nil
}

func pickDestinationItemID(book database.Book, destinationItemIDs map[int64]string) string {
	if id := strings.TrimSpace(destinationItemIDs[book.ID]); id != "" {
		return id
	}
	return ""
}

func upsertBookDestinationItem(ctx context.Context, db database.Database, bookID int64, destinationID, serverItemID, serverItemTitle string) error {
	bd, err := db.GetBookDestination(ctx, bookID, destinationID)
	if err != nil {
		return err
	}
	if bd == nil {
		bd = &database.BookDestination{BookID: bookID, DestinationID: destinationID}
	}
	bd.ServerItemID = strings.TrimSpace(serverItemID)
	bd.ServerItemTitle = strings.TrimSpace(serverItemTitle)
	// Reconcile proved the destination has this item. Promote pending→synced
	// so the UI badge flips from "Pending" to "Synced" without waiting for
	// a sync attempt. Don't clobber failed/orphaned — those carry signal.
	if bd.ServerItemID != "" && (bd.SyncState == "" || bd.SyncState == database.BookDestSyncPending) {
		bd.SyncState = database.BookDestSyncSynced
		now := time.Now().UTC()
		bd.LastSucceededAt = &now
	}
	return db.UpsertBookDestination(ctx, bd)
}