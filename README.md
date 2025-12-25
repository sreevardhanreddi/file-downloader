# File Downloader

A simple web-based file downloader built with FastAPI, HTMX, and Tailwind CSS.

## Features

- Paste any URL to download files (PDFs, videos, etc.)
- Real-time download progress with speed and ETA
- **Pause/Resume** downloads with HTTP Range support
- **Cancel** downloads and clean up partial files
- **Retry** failed or cancelled downloads
- Background downloads with async processing
- SQLite database for persistent download history
- Rename downloaded files
- Delete options: remove from database only or delete file + database entry
- Dark mode UI (Tailwind stone theme)
- Responsive design

## Installation

```bash
# Install dependencies
pip install -r requirements.txt

# Run the application
uvicorn main:app --reload
```

Visit `http://localhost:8000` in your browser.

## Tech Stack

- **Backend**: FastAPI, Python 3.10+
- **Frontend**: HTMX, Tailwind CSS
- **Database**: SQLite (aiosqlite)
- **HTTP Client**: httpx (async)

## Project Structure

```
.
├── main.py              # FastAPI application
├── database.py          # SQLite database operations
├── templates/
│   ├── index.html       # Main page template
│   └── partials/
│       └── download_list.html  # HTMX partial for download list
├── downloads/           # Downloaded files directory
└── requirements.txt     # Python dependencies
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

## License

MIT
