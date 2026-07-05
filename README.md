# SESystem

SESystem is a small Go + SQLite + HTMX web application for managing jobs, clients, finances, receipts, and basic team access for a solo service business. The app is currently being shaped as a lightweight SaSS-style workspace with a public login screen and role-based sections after sign-in.

## Current Product Direction

- The public experience is intentionally minimal: visitors only see the login page.
- After login, the app exposes a workspace with different section sets depending on the user role.
- Admins can manage the broader business operations.
- Standard users get a focused day-to-day workflow centered on jobs and related views.

## Features

- Local-only admin access for protected routes and logout
- Username/password authentication with seeded demo accounts
- Role-based access:
  - Admin: jobs, clients, receipts, spreadsheets, finances, users
  - User: jobs, clients, receipts, spreadsheets
- Public receipt endpoint for completed jobs
- Simple SQLite-backed persistence for users, clients, jobs, sessions
- Responsive UI built with Pico.css and HTMX

## Default Login

Use the following credentials to sign in locally:

- Username: admin
- Password: admin123

There is also a regular user demo account:

- Username: user
- Password: user123

## Run Locally

```bash
cd /home/okarun/Dev/sesystem
go mod tidy
go build ./...
./sesystem
```

Then open:

```text
http://localhost:8080
```

The root page is the public login screen. After authentication, the workspace becomes available.

## Architecture Notes

- Main server and routing live in main.go.
- Templates are split into reusable layout files in templates/ and rendered by the Go server.
- Static styling is in static/styles.css.
- The SQLite database file is sesystem.db.
- Session-based authentication is stored in the database and checked on each request.
- Role checks are handled in the server layer and reflected in the templates via the IsAdmin flag.

## Security Model

- The Go server binds to 127.0.0.1:8080 only.
- Protected routes are restricted to localhost requests.
- Admin-only actions and pages are guarded by role checks.
- The receipt route is public-facing and does not expose the rest of the dashboard.

## Current Implementation Notes

- The app uses Go's standard library HTTP server with HTMX for progressive interactions.
- The UI is intentionally simple and currently focused on usability over polish.
- The navigation is shared from templates/base.html and should be updated there if the section list changes.
- New pages should be added as new templates and routed in main.go.
- The database schema is initialized in initDB and includes users, clients, jobs, sessions.

## Verification

You can verify the app with:

```bash
go test ./...
go build ./...
curl -i http://127.0.0.1:8080/
```

## Notes

The application uses SQLite and stores its data in sesystem.db. Future agents should preserve the current role-based separation and keep the public login screen minimal.

