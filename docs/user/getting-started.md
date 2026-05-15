# Getting Started

This gets Audplexus running fast, then ready to process books.

## 1. Start the App

Open the web UI at:

`http://localhost:8080`

![Dashboard first load](./screenshots/getting-started-open-dashboard-initial-view.png)

If using Docker Compose, start from your project folder:

```bash
docker compose up -d
```

## 2. Connect Audible

1. Go to Settings.
2. Pick your Audible marketplace.
3. Start sign-in and complete auth.

When connected, Audplexus can sync your library metadata.

![Settings audible marketplace select](./screenshots/getting-started-auth-marketplace-select.png)
![Settings audible auth start](./screenshots/getting-started-auth-signin-start.png)

## 3. Add a Library Destination (Optional but Recommended)

If you want books to appear in Plex/Emby/Jellyfin/Audiobookshelf automatically:

1. Go to Settings -> Library Destinations.
2. Add a destination.
3. Test connection.
4. Save.

Detailed steps are in [Connect a Library Destination](./connect-library-destination.md).

![Destinations add entry point](./screenshots/getting-started-destinations-list-add.png)

## 4. Run a Sync

1. Open Dashboard or Library.
2. Run sync.
3. Wait for phases to complete.

Use [Sync Your Library](./sync-library.md) for quick vs full sync guidance.

![Dashboard sync button run](./screenshots/getting-started-sync-controls-run.png)

## 5. Queue Books

1. Queue one book or queue all new books.
2. Watch progress in Downloads.
3. Let pipeline finish.

More in [Queue and Download Books](./downloads-and-queue.md).

![Library queue action](./screenshots/getting-started-library-book-actions-queue.png)

