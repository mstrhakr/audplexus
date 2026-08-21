package mediaserver

import (
	"context"
	"testing"
	"time"

	"github.com/mstrhakr/audplexus/internal/database"
)

func TestPickDestinationItemIDPrefersStoredDestinationID(t *testing.T) {
	t.Parallel()

	book := database.Book{ID: 1}
	got := pickDestinationItemID(book, map[int64]string{1: "stored"})
	if got != "stored" {
		t.Fatalf("pickDestinationItemID() = %q, want %q", got, "stored")
	}
}

func TestLoadBookDestinationItemIDs(t *testing.T) {
	t.Parallel()

	db := &destinationIDsStubDB{rows: []database.BookDestination{{BookID: 1, ServerItemID: "a"}, {BookID: 2, ServerItemID: ""}, {BookID: 3, ServerItemID: "b"}}}
	got, err := loadBookDestinationItemIDs(context.Background(), db, "dest")
	if err != nil {
		t.Fatalf("loadBookDestinationItemIDs() error = %v", err)
	}
	if got[1] != "a" || got[3] != "b" || len(got) != 2 {
		t.Fatalf("loadBookDestinationItemIDs() = %#v", got)
	}
}

type destinationIDsStubDB struct {
	rows []database.BookDestination
}

func (d *destinationIDsStubDB) ListBookDestinationsBy(ctx context.Context, destinationID string, state *database.BookDestinationSyncState) ([]database.BookDestination, error) {
	return d.rows, nil
}

func (d *destinationIDsStubDB) Close() error { return nil }
func (d *destinationIDsStubDB) Migrate() error { return nil }
func (d *destinationIDsStubDB) Reset(ctx context.Context) error { return nil }
func (d *destinationIDsStubDB) GetBook(ctx context.Context, id int64) (*database.Book, error) { return nil, nil }
func (d *destinationIDsStubDB) GetBookByASIN(ctx context.Context, asin string) (*database.Book, error) { return nil, nil }
func (d *destinationIDsStubDB) ListBooks(ctx context.Context, filter database.BookFilter) ([]database.Book, int, error) { return nil, 0, nil }
func (d *destinationIDsStubDB) CountBooksByStatus(ctx context.Context) (map[database.BookStatus]int, error) { return nil, nil }
func (d *destinationIDsStubDB) UpsertBook(ctx context.Context, book *database.Book) error { return nil }
func (d *destinationIDsStubDB) UpdateBookStatus(ctx context.Context, id int64, status database.BookStatus) error { return nil }
func (d *destinationIDsStubDB) DeleteBook(ctx context.Context, id int64) error { return nil }
func (d *destinationIDsStubDB) EnqueueDownload(ctx context.Context, item *database.DownloadQueue) error { return nil }
func (d *destinationIDsStubDB) GetNextPendingDownload(ctx context.Context) (*database.DownloadQueue, error) { return nil, nil }
func (d *destinationIDsStubDB) UpdateDownload(ctx context.Context, item *database.DownloadQueue) error { return nil }
func (d *destinationIDsStubDB) ListDownloads(ctx context.Context, status *database.DownloadStatus) ([]database.DownloadQueue, error) { return nil, nil }
func (d *destinationIDsStubDB) CancelDownload(ctx context.Context, id int64) error { return nil }
func (d *destinationIDsStubDB) RetryDownload(ctx context.Context, id int64) error { return nil }
func (d *destinationIDsStubDB) RetryAllDownloads(ctx context.Context) (int64, error) { return 0, nil }
func (d *destinationIDsStubDB) CreateSync(ctx context.Context, sync *database.SyncHistory) error { return nil }
func (d *destinationIDsStubDB) UpdateSync(ctx context.Context, sync *database.SyncHistory) error { return nil }
func (d *destinationIDsStubDB) GetLastSync(ctx context.Context) (*database.SyncHistory, error) { return nil, nil }
func (d *destinationIDsStubDB) GetSetting(ctx context.Context, key string) (string, error) { return "", nil }
func (d *destinationIDsStubDB) SetSetting(ctx context.Context, key, value string) error { return nil }
func (d *destinationIDsStubDB) GetActiveDevice(ctx context.Context) (*database.Device, error) { return nil, nil }
func (d *destinationIDsStubDB) SaveDevice(ctx context.Context, device *database.Device) error { return nil }
func (d *destinationIDsStubDB) ListDevices(ctx context.Context) ([]database.Device, error) { return nil, nil }
func (d *destinationIDsStubDB) DeleteDevice(ctx context.Context, id int64) error { return nil }
func (d *destinationIDsStubDB) CreateAudibleAccount(ctx context.Context, a *database.AudibleAccount) error { return nil }
func (d *destinationIDsStubDB) GetAudibleAccount(ctx context.Context, id string) (*database.AudibleAccount, error) { return nil, nil }
func (d *destinationIDsStubDB) GetAudibleAccountByCustomerID(ctx context.Context, customerID string) (*database.AudibleAccount, error) { return nil, nil }
func (d *destinationIDsStubDB) ListAudibleAccounts(ctx context.Context) ([]database.AudibleAccount, error) { return nil, nil }
func (d *destinationIDsStubDB) ListEnabledAudibleAccounts(ctx context.Context) ([]database.AudibleAccount, error) { return nil, nil }
func (d *destinationIDsStubDB) UpdateAudibleAccount(ctx context.Context, a *database.AudibleAccount) error { return nil }
func (d *destinationIDsStubDB) DeleteAudibleAccount(ctx context.Context, id string) error { return nil }
func (d *destinationIDsStubDB) SetBookAccount(ctx context.Context, asin, accountID string) error { return nil }
func (d *destinationIDsStubDB) GetBookAccount(ctx context.Context, asin string) (string, error) { return "", nil }
func (d *destinationIDsStubDB) ReplaceBookAccounts(ctx context.Context, asin string, accountIDs []string) error { return nil }
func (d *destinationIDsStubDB) GetBookAccountsForASINs(ctx context.Context, asins []string) (map[string][]string, error) { return map[string][]string{}, nil }
func (d *destinationIDsStubDB) ListASINsForAccount(ctx context.Context, accountID string) ([]string, error) { return nil, nil }
func (d *destinationIDsStubDB) CreateLibraryDestination(ctx context.Context, d2 *database.LibraryDestination) error { return nil }
func (d *destinationIDsStubDB) GetLibraryDestination(ctx context.Context, id string) (*database.LibraryDestination, error) { return nil, nil }
func (d *destinationIDsStubDB) ListLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error) { return nil, nil }
func (d *destinationIDsStubDB) ListEnabledLibraryDestinations(ctx context.Context) ([]database.LibraryDestination, error) { return nil, nil }
func (d *destinationIDsStubDB) UpdateLibraryDestination(ctx context.Context, d2 *database.LibraryDestination) error { return nil }
func (d *destinationIDsStubDB) DeleteLibraryDestination(ctx context.Context, id string) error { return nil }
func (d *destinationIDsStubDB) UpsertBookDestination(ctx context.Context, bd *database.BookDestination) error { return nil }
func (d *destinationIDsStubDB) GetBookDestinations(ctx context.Context, bookID int64) ([]database.BookDestination, error) { return nil, nil }
func (d *destinationIDsStubDB) GetBookDestination(ctx context.Context, bookID int64, destID string) (*database.BookDestination, error) { return nil, nil }

func (d *destinationIDsStubDB) GetUserByUsername(ctx context.Context, u string) (*database.User, error) { return nil, nil }
func (d *destinationIDsStubDB) GetUserByID(ctx context.Context, id int64) (*database.User, error) { return nil, nil }
func (d *destinationIDsStubDB) GetFirstUser(ctx context.Context) (*database.User, error) { return nil, nil }
func (d *destinationIDsStubDB) GetUserByOIDC(ctx context.Context, issuer, subject string) (*database.User, error) { return nil, nil }
func (d *destinationIDsStubDB) CountUsers(ctx context.Context) (int, error) { return 0, nil }
func (d *destinationIDsStubDB) UpsertUser(ctx context.Context, u *database.User) error { return nil }
func (d *destinationIDsStubDB) RotateUserIdentifier(ctx context.Context, id int64, ident string) error { return nil }
func (d *destinationIDsStubDB) DeleteUser(ctx context.Context, id int64) error { return nil }
func (d *destinationIDsStubDB) CreateSession(ctx context.Context, sess *database.Session) error { return nil }
func (d *destinationIDsStubDB) GetSession(ctx context.Context, token string) (*database.Session, error) { return nil, nil }
func (d *destinationIDsStubDB) TouchSession(ctx context.Context, token string, t time.Time) error { return nil }
func (d *destinationIDsStubDB) DeleteSession(ctx context.Context, token string) error { return nil }
func (d *destinationIDsStubDB) DeleteSessionsForUser(ctx context.Context, userID int64) error { return nil }
func (d *destinationIDsStubDB) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) { return 0, nil }