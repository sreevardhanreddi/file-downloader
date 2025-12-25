import logging
import re
from pathlib import Path
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


templates.env.filters["humanize_bytes"] = humanize_bytes


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


async def download_file(download_id: int, url: str):
    """Background task to download a file."""
    logger.info(f"[#{download_id}] Starting download: {url}")

    try:
        await update_download(download_id, status=DownloadStatus.DOWNLOADING.value)

        async with httpx.AsyncClient(follow_redirects=True, timeout=None) as client:
            async with client.stream("GET", url) as response:
                response.raise_for_status()
                logger.info(
                    f"[#{download_id}] Connected - Status: {response.status_code}"
                )

                filename = extract_filename(url, dict(response.headers))
                file_size = int(response.headers.get("content-length", 0))

                # Ensure unique filename
                filepath = DOWNLOADS_DIR / filename
                counter = 1
                while filepath.exists():
                    stem = Path(filename).stem
                    suffix = Path(filename).suffix
                    filepath = DOWNLOADS_DIR / f"{stem}_{counter}{suffix}"
                    counter += 1

                await update_download(
                    download_id, filename=filepath.name, file_size=file_size
                )

                size_str = humanize_bytes(file_size) if file_size else "Unknown size"
                logger.info(f"[#{download_id}] Saving as: {filepath.name} ({size_str})")

                downloaded = 0
                last_progress = 0
                last_log_progress = 0

                with open(filepath, "wb") as f:
                    async for chunk in response.aiter_bytes(chunk_size=8192):
                        f.write(chunk)
                        downloaded += len(chunk)

                        if file_size > 0:
                            progress = int((downloaded / file_size) * 100)
                            if progress != last_progress and progress % 5 == 0:
                                await update_download(download_id, progress=progress)
                                last_progress = progress

                            # Log every 10%
                            if progress >= last_log_progress + 10:
                                logger.info(
                                    f"[#{download_id}] Progress: {progress}% "
                                    f"({humanize_bytes(downloaded)} / {humanize_bytes(file_size)})"
                                )
                                last_log_progress = progress

                await update_download(
                    download_id, status=DownloadStatus.COMPLETED.value, progress=100
                )
                logger.info(
                    f"[#{download_id}] Completed: {filepath.name} ({humanize_bytes(file_size)})"
                )

    except Exception as e:
        logger.error(f"[#{download_id}] Failed: {str(e)}")
        await update_download(
            download_id, status=DownloadStatus.FAILED.value, error=str(e)
        )


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
