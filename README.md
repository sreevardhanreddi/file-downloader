# File Downloader

A simple web-based file downloader built with Go, HTMX, and Tailwind CSS.

## Features

- Paste any URL to download files (PDFs, videos, etc.)
- Real-time download progress with speed and ETA
- **Pause/Resume** downloads with HTTP Range support
- **Cancel** downloads and clean up partial files
- **Retry** failed or cancelled downloads
- Background downloads with goroutines
- SQLite database for persistent download history
- Rename downloaded files
- Delete options: remove from database only or delete file + database entry
- Dark mode UI (Tailwind stone theme)
- Responsive design

## Installation

### Prerequisites

- Go 1.22 or higher (Go 1.23+ recommended for Docker development with hot-reload)
- GCC compiler (for SQLite CGO dependency)

### Build and Run

```bash
# Install dependencies
go mod tidy

# Run the application
go run main.go

# Or build and run
go build -o wgetter
./wgetter
```

Visit `http://localhost:8000` in your browser.

## Tech Stack

- **Backend**: Go (standard library net/http)
- **Frontend**: HTMX, Tailwind CSS
- **Database**: SQLite (github.com/mattn/go-sqlite3)
- **Templates**: Go html/template

## Project Structure

```
.
├── main.go                      # Main application and HTTP handlers
├── database/
│   └── database.go              # SQLite database operations
├── templates/
│   ├── index.html               # Main page template
│   └── download_list.html       # HTMX partial for download list
├── downloads/                   # Downloaded files directory
└── downloads.db                 # SQLite database (created on first run)
```

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/` | Main page |
| POST | `/download` | Start a new download |
| GET | `/downloads` | Get download list (HTMX polling) |
| POST | `/download/{id}/pause` | Pause an active download |
| POST | `/download/{id}/resume` | Resume a paused download |
| POST | `/download/{id}/cancel` | Cancel and remove partial file |
| POST | `/download/{id}/retry` | Retry failed/cancelled download |
| PUT | `/download/{id}/rename` | Rename a file |
| DELETE | `/download/{id}` | Delete from DB and filesystem |
| DELETE | `/download/{id}/db-only` | Delete from DB only |
| GET | `/file/{id}` | Serve downloaded file |
| GET | `/view/{id}` | View file inline in browser |

## Building for Production

```bash
# Build optimized binary
go build -ldflags="-s -w" -o wgetter

# Cross-compile for different platforms
# Linux
GOOS=linux GOARCH=amd64 go build -o wgetter-linux

# Windows
GOOS=windows GOARCH=amd64 go build -o wgetter.exe

# macOS
GOOS=darwin GOARCH=amd64 go build -o wgetter-mac
```

## Environment Variables

- `PORT`: Server port (default: 8000)

Example:
```bash
PORT=3000 go run main.go
```

## Development

```bash
# Run with auto-reload (using air)
go install github.com/cosmtrek/air@latest
air

# Run tests
go test ./...

# Format code
go fmt ./...
```

## Docker

### Production Deployment

```bash
# Build and run with Docker Compose
docker-compose -f docker-compose.prod.yml up -d

# Or build manually
docker build -f Dockerfile.prod -t wgetter .
docker run -p 8000:8000 -v $(pwd)/downloads:/app/downloads wgetter
```

### Development with Hot-Reload

```bash
# Run with Docker Compose (includes air for hot-reload)
docker-compose -f docker-compose.dev.yml up

# Access at http://localhost:8000
```

The Docker setup includes:
- Multi-stage builds for minimal image size
- Volume mounts for persistent downloads and database
- Health checks for production
- Hot-reload support for development

## Performance Notes

Key advantages of the Go implementation:

1. **Lower Memory Usage**: ~20 MB vs typical web frameworks (50-100 MB)
2. **Faster Startup**: Instant startup, no interpreter overhead
3. **Better Concurrency**: Native goroutines for efficient concurrent downloads
4. **Single Binary**: No runtime dependencies, easy deployment
5. **Cross-Platform**: Compile once for any platform

## License

MIT
