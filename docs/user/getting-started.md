# Getting Started

This is the short path to get Audplexus running and ready for real work.

## Before you start

Make sure these are true before you begin:

- the app is running and you can open the web UI
- your audiobook output folders are writable
- your Audible account is ready to authenticate
- optional: your media server is available if you want automatic destination syncing

## 1. Start the app

Open the web UI at:

http://localhost:8080

If you are using Docker Compose, run the standard start command from the project folder:

```bash
docker compose up -d
```

![Dashboard first load](./screenshots/getting-started-open-dashboard-initial-view.png)

## 2. Connect Audible

1. Open Settings.
2. Choose the correct Audible marketplace.
3. Start sign-in and complete the login flow.

Once connected, Audplexus can read your Audible library and keep it in sync.

![Settings audible marketplace select](./screenshots/getting-started-auth-marketplace-select.png)
![Settings audible auth start](./screenshots/getting-started-auth-signin-start.png)

## 3. Add a library destination

This is optional, but strongly recommended if you want books to be pushed automatically to Plex, Emby, Jellyfin, or Audiobookshelf.

1. Go to Settings -> Library Destinations.
2. Add a destination.
3. Test the connection.
4. Save the configuration.

Use [Connect a Library Destination](./connect-library-destination.md) for the detailed steps.

![Destinations add entry point](./screenshots/getting-started-destinations-list-add.png)

## 4. Run a sync

1. Open the Dashboard or Library view.
2. Start a quick or full sync.
3. Wait for the phases to complete.

See [Sync Your Library](./sync-library.md) for the difference between quick and full sync.

![Dashboard sync button run](./screenshots/getting-started-sync-controls-run.png)

## 5. Queue books

1. Queue a single book or queue all new books.
2. Monitor the processing flow in Downloads.
3. Let the pipeline finish.

See [Queue and Download Books](./downloads-and-queue.md) for the detailed lifecycle and management actions.

![Library queue action](./screenshots/getting-started-library-book-actions-queue.png)

## Recommended first pass

For a first setup, do this in order:

1. connect Audible
2. verify a library sync works
3. add one destination and confirm it connects
4. queue a single test book
5. confirm the file is organized and appears in the destination

That path gives you the clearest signal that the core workflow is working before you expand to larger library use.

