package mediaserver

import (
	"context"
	"strings"

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
	if id := strings.TrimSpace(book.MediaServerID); id != "" {
		return id
	}
	return strings.TrimSpace(book.PlexRatingKey)
}