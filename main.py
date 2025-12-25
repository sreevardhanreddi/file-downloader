import asyncio
import logging
import re
import time
from pathlib import Path
from typing import Dict
from urllib.parse import unquote, urlparse

import httpx

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s | %(levelname)-8s | %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger("file-downloader")
from fastapi import BackgroundTasks, FastAPI, Form, Request
from fastapi.responses import FileResponse, HTMLResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates

from database import (
    DownloadStatus,
    add_download,
    delete_download,
    get_download,
    get_downloads,
    init_db,
    update_download,
)

app = FastAPI(title="File Downloader")

DOWNLOADS_DIR = Path(__file__).parent / "downloads"
DOWNLOADS_DIR.mkdir(exist_ok=True)

# Track active download tasks for pause/cancel
active_downloads: Dict[int, asyncio.Event] = {}  # download_id -> cancel_event

templates = Jinja2Templates(directory="templates")


def humanize_bytes(size: int) -> str:
    """Convert bytes to human readable format."""
    if size is None:
        return ""
    for unit in ["B", "KB", "MB", "GB", "TB"]:
        if abs(size) < 1024:
            return f"{size:.2f} {unit}"
        size /= 1024
    return f"{size:.2f} PB"


def format_eta(seconds: int) -> str:
    """Convert seconds to human readable ETA."""
    if seconds is None or seconds <= 0:
        return ""
    if seconds < 60:
        return f"{seconds}s"
    elif seconds < 3600:
        minutes = seconds // 60
        secs = seconds % 60
        return f"{minutes}m {secs}s"
    else:
        hours = seconds // 3600
        minutes = (seconds % 3600) // 60
        return f"{hours}h {minutes}m"


templates.env.filters["humanize_bytes"] = humanize_bytes
templates.env.filters["format_eta"] = format_eta


@app.on_event("startup")
async def startup():
    await init_db()


def extract_filename(url: str, headers: dict) -> str:
    """Extract filename from Content-Disposition header or URL."""
    content_disposition = headers.get("content-disposition", "")
    if content_disposition:
        match = re.search(r'filename[^;=\n]*=(["\']?)([^"\';]+)\1', content_disposition)
        if match:
            return match.group(2)

    parsed = urlparse(url)
    path = unquote(parsed.path)
    if path and "/" in path:
        return path.split("/")[-1] or "download"
    return "download"


async def download_file(download_id: int, url: str, resume_from: int = 0):
    """Background task to download a file."""
    action = "Resuming" if resume_from > 0 else "Starting"
    logger.info(f"[#{download_id}] {action} download: {url}")

    # Create cancel event for this download
    cancel_event = asyncio.Event()
    active_downloads[download_id] = cancel_event

    try:
        await update_download(download_id, status=DownloadStatus.DOWNLOADING.value)

        # Get existing download info for resume
        download_info = await get_download(download_id)
        existing_filename = download_info.get("filename") if download_info else None

        headers = {}
        if resume_from > 0:
            headers["Range"] = f"bytes={resume_from}-"

        async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
            async with client.stream("GET", url, headers=headers) as response:
                response.raise_for_status()
                logger.info(
                    f"[#{download_id}] Connected - Status: {response.status_code}"
                )

                # For resumed downloads, use existing filename
                if existing_filename:
                    filename = existing_filename
                    filepath = DOWNLOADS_DIR / filename
                else:
                    filename = extract_filename(url, dict(response.headers))
                    filepath = DOWNLOADS_DIR / filename
                    counter = 1
                    while filepath.exists():
                        stem = Path(filename).stem
                        suffix = Path(filename).suffix
                        filepath = DOWNLOADS_DIR / f"{stem}_{counter}{suffix}"
                        counter += 1

                # Get file size (for Range requests, need to parse Content-Range)
                if response.status_code == 206:  # Partial content
                    content_range = response.headers.get("content-range", "")
                    if "/" in content_range:
                        file_size = int(content_range.split("/")[-1])
                    else:
                        file_size = (
                            download_info.get("file_size", 0) if download_info else 0
                        )
                else:
                    file_size = int(response.headers.get("content-length", 0))

                await update_download(
                    download_id, filename=filepath.name, file_size=file_size
                )

                size_str = humanize_bytes(file_size) if file_size else "Unknown size"
                logger.info(f"[#{download_id}] Saving as: {filepath.name} ({size_str})")

                downloaded = resume_from
                last_progress = 0
                last_log_progress = (
                    (resume_from * 100 // file_size) if file_size > 0 else 0
                )
                start_time = time.time()
                last_update_time = start_time

                # Open in append mode if resuming, write mode otherwise
                mode = "ab" if resume_from > 0 else "wb"
                with open(filepath, mode) as f:
                    async for chunk in response.aiter_bytes(chunk_size=8192):
                        # Check for cancellation/pause
                        if cancel_event.is_set():
                            # Save progress before stopping
                            await update_download(
                                download_id,
                                downloaded_bytes=downloaded,
                                speed=0,
                                eta=0,
                            )
                            logger.info(
                                f"[#{download_id}] Download stopped at {downloaded} bytes"
                            )
                            return

                        f.write(chunk)
                        downloaded += len(chunk)

                        if file_size > 0:
                            progress = int((downloaded / file_size) * 100)
                            current_time = time.time()

                            # Update every 5% or every second
                            if (progress != last_progress and progress % 5 == 0) or (
                                current_time - last_update_time >= 1
                            ):
                                elapsed = current_time - start_time
                                speed = (
                                    int((downloaded - resume_from) / elapsed)
                                    if elapsed > 0
                                    else 0
                                )
                                remaining = file_size - downloaded
                                eta = int(remaining / speed) if speed > 0 else 0

                                await update_download(
                                    download_id,
                                    progress=progress,
                                    downloaded_bytes=downloaded,
                                    speed=speed,
                                    eta=eta,
                                )
                                last_progress = progress
                                last_update_time = current_time

                            # Log every 10%
                            if progress >= last_log_progress + 10:
                                elapsed = current_time - start_time
                                speed = (
                                    int((downloaded - resume_from) / elapsed)
                                    if elapsed > 0
                                    else 0
                                )
                                eta = (
                                    int((file_size - downloaded) / speed)
                                    if speed > 0
                                    else 0
                                )
                                logger.info(
                                    f"[#{download_id}] Progress: {progress}% "
                                    f"({humanize_bytes(downloaded)} / {humanize_bytes(file_size)}) "
                                    f"- {humanize_bytes(speed)}/s - ETA: {format_eta(eta)}"
                                )
                                last_log_progress = progress

                await update_download(
                    download_id,
                    status=DownloadStatus.COMPLETED.value,
                    progress=100,
                    downloaded_bytes=downloaded,
                )
                logger.info(
                    f"[#{download_id}] Completed: {filepath.name} ({humanize_bytes(file_size)})"
                )

    except Exception as e:
        logger.error(f"[#{download_id}] Failed: {str(e)}")
        await update_download(
            download_id, status=DownloadStatus.FAILED.value, error=str(e)
        )
    finally:
        # Clean up
        if download_id in active_downloads:
            del active_downloads[download_id]


@app.get("/", response_class=HTMLResponse)
async def index(request: Request):
    downloads = await get_downloads()
    return templates.TemplateResponse(
        "index.html", {"request": request, "downloads": downloads}
    )


@app.post("/download", response_class=HTMLResponse)
async def start_download(
    request: Request, background_tasks: BackgroundTasks, url: str = Form(...)
):
    download_id = await add_download(url)
    background_tasks.add_task(download_file, download_id, url)

    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.get("/downloads", response_class=HTMLResponse)
async def list_downloads(request: Request):
    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.post("/download/{download_id}/pause", response_class=HTMLResponse)
async def pause_download(request: Request, download_id: int):
    """Pause an active download."""
    if download_id in active_downloads:
        active_downloads[download_id].set()  # Signal to stop
        await update_download(download_id, status=DownloadStatus.PAUSED.value)
        logger.info(f"[#{download_id}] Paused")

    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.post("/download/{download_id}/resume", response_class=HTMLResponse)
async def resume_download(
    request: Request, download_id: int, background_tasks: BackgroundTasks
):
    """Resume a paused download."""
    download = await get_download(download_id)
    if download and download.get("status") == DownloadStatus.PAUSED.value:
        resume_from = download.get("downloaded_bytes", 0) or 0
        background_tasks.add_task(
            download_file, download_id, download["url"], resume_from
        )
        logger.info(f"[#{download_id}] Resuming from {humanize_bytes(resume_from)}")

    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.post("/download/{download_id}/cancel", response_class=HTMLResponse)
async def cancel_download(request: Request, download_id: int):
    """Cancel a download and remove partial file."""
    download = await get_download(download_id)

    # Stop active download if running
    if download_id in active_downloads:
        active_downloads[download_id].set()
        # Give it a moment to stop
        await asyncio.sleep(0.1)

    # Remove partial file
    if download and download.get("filename"):
        filepath = DOWNLOADS_DIR / download["filename"]
        if filepath.exists():
            filepath.unlink()

    await update_download(
        download_id,
        status=DownloadStatus.CANCELLED.value,
        downloaded_bytes=0,
        progress=0,
        speed=0,
        eta=0,
    )
    logger.info(f"[#{download_id}] Cancelled")

    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.post("/download/{download_id}/retry", response_class=HTMLResponse)
async def retry_download(
    request: Request, download_id: int, background_tasks: BackgroundTasks
):
    """Retry a failed or cancelled download from the beginning."""
    download = await get_download(download_id)
    if download and download.get("status") in [
        DownloadStatus.FAILED.value,
        DownloadStatus.CANCELLED.value,
    ]:
        # Remove old partial file if exists
        if download.get("filename"):
            filepath = DOWNLOADS_DIR / download["filename"]
            if filepath.exists():
                filepath.unlink()

        # Reset and restart
        await update_download(
            download_id,
            status=DownloadStatus.PENDING.value,
            progress=0,
            downloaded_bytes=0,
            error=None,
        )
        background_tasks.add_task(download_file, download_id, download["url"], 0)
        logger.info(f"[#{download_id}] Retrying download")

    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.put("/download/{download_id}/rename", response_class=HTMLResponse)
async def rename_download(
    request: Request, download_id: int, new_filename: str = Form(...)
):
    download = await get_download(download_id)
    if not download or not download.get("filename"):
        downloads = await get_downloads()
        return templates.TemplateResponse(
            "partials/download_list.html", {"request": request, "downloads": downloads}
        )

    old_filepath = DOWNLOADS_DIR / download["filename"]
    if not old_filepath.exists():
        downloads = await get_downloads()
        return templates.TemplateResponse(
            "partials/download_list.html", {"request": request, "downloads": downloads}
        )

    # Preserve file extension if not provided
    old_suffix = old_filepath.suffix
    new_path = Path(new_filename)
    if not new_path.suffix:
        new_filename = new_filename + old_suffix

    new_filepath = DOWNLOADS_DIR / new_filename

    # Ensure unique filename
    counter = 1
    base_new_filepath = new_filepath
    while new_filepath.exists() and new_filepath != old_filepath:
        stem = base_new_filepath.stem
        suffix = base_new_filepath.suffix
        new_filepath = DOWNLOADS_DIR / f"{stem}_{counter}{suffix}"
        counter += 1

    old_filepath.rename(new_filepath)
    await update_download(download_id, filename=new_filepath.name)

    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.delete("/download/{download_id}", response_class=HTMLResponse)
async def remove_download_full(request: Request, download_id: int):
    """Delete from both database and filesystem."""
    download = await get_download(download_id)
    if download and download.get("filename"):
        filepath = DOWNLOADS_DIR / download["filename"]
        if filepath.exists():
            filepath.unlink()

    await delete_download(download_id)
    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.delete("/download/{download_id}/db-only", response_class=HTMLResponse)
async def remove_download_db_only(request: Request, download_id: int):
    """Delete from database only, keep file on filesystem."""
    await delete_download(download_id)
    downloads = await get_downloads()
    return templates.TemplateResponse(
        "partials/download_list.html", {"request": request, "downloads": downloads}
    )


@app.get("/file/{download_id}")
async def serve_file(download_id: int):
    download = await get_download(download_id)
    if not download or not download.get("filename"):
        return HTMLResponse("File not found", status_code=404)

    filepath = DOWNLOADS_DIR / download["filename"]
    if not filepath.exists():
        return HTMLResponse("File not found", status_code=404)

    return FileResponse(filepath, filename=download["filename"])


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=8000)
