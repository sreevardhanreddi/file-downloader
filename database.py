from datetime import datetime
from enum import Enum
from pathlib import Path

import aiosqlite

DATABASE_PATH = Path(__file__).parent / "downloads.db"


class DownloadStatus(str, Enum):
    PENDING = "pending"
    DOWNLOADING = "downloading"
    PAUSED = "paused"
    COMPLETED = "completed"
    CANCELLED = "cancelled"
    FAILED = "failed"


async def init_db():
    async with aiosqlite.connect(DATABASE_PATH) as db:
        await db.execute(
            """
            CREATE TABLE IF NOT EXISTS downloads (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                url TEXT NOT NULL,
                filename TEXT,
                status TEXT DEFAULT 'pending',
                progress INTEGER DEFAULT 0,
                file_size INTEGER,
                downloaded_bytes INTEGER DEFAULT 0,
                speed INTEGER,
                eta INTEGER,
                error TEXT,
                created_at TEXT DEFAULT CURRENT_TIMESTAMP,
                completed_at TEXT
            )
        """
        )
        # Add columns if they don't exist (for existing databases)
        for column in [
            "speed INTEGER",
            "eta INTEGER",
            "downloaded_bytes INTEGER DEFAULT 0",
        ]:
            try:
                await db.execute(f"ALTER TABLE downloads ADD COLUMN {column}")
            except Exception:
                pass
        await db.commit()


async def add_download(url: str) -> int:
    async with aiosqlite.connect(DATABASE_PATH) as db:
        cursor = await db.execute(
            "INSERT INTO downloads (url, status) VALUES (?, ?)",
            (url, DownloadStatus.PENDING.value),
        )
        await db.commit()
        return cursor.lastrowid


async def update_download(
    download_id: int,
    status: str = None,
    progress: int = None,
    filename: str = None,
    file_size: int = None,
    downloaded_bytes: int = None,
    speed: int = None,
    eta: int = None,
    error: str = None,
):
    async with aiosqlite.connect(DATABASE_PATH) as db:
        updates = []
        params = []

        if status:
            updates.append("status = ?")
            params.append(status)
            if status == DownloadStatus.COMPLETED.value:
                updates.append("completed_at = ?")
                params.append(datetime.now().isoformat())
                updates.append("eta = ?")
                params.append(0)
                updates.append("speed = ?")
                params.append(0)
        if progress is not None:
            updates.append("progress = ?")
            params.append(progress)
        if filename:
            updates.append("filename = ?")
            params.append(filename)
        if file_size is not None:
            updates.append("file_size = ?")
            params.append(file_size)
        if downloaded_bytes is not None:
            updates.append("downloaded_bytes = ?")
            params.append(downloaded_bytes)
        if speed is not None:
            updates.append("speed = ?")
            params.append(speed)
        if eta is not None:
            updates.append("eta = ?")
            params.append(eta)
        if error:
            updates.append("error = ?")
            params.append(error)

        if updates:
            params.append(download_id)
            await db.execute(
                f"UPDATE downloads SET {', '.join(updates)} WHERE id = ?", params
            )
            await db.commit()


async def get_downloads():
    async with aiosqlite.connect(DATABASE_PATH) as db:
        db.row_factory = aiosqlite.Row
        cursor = await db.execute("SELECT * FROM downloads ORDER BY created_at DESC")
        rows = await cursor.fetchall()
        return [dict(row) for row in rows]


async def get_download(download_id: int):
    async with aiosqlite.connect(DATABASE_PATH) as db:
        db.row_factory = aiosqlite.Row
        cursor = await db.execute(
            "SELECT * FROM downloads WHERE id = ?", (download_id,)
        )
        row = await cursor.fetchone()
        return dict(row) if row else None


async def delete_download(download_id: int):
    async with aiosqlite.connect(DATABASE_PATH) as db:
        await db.execute("DELETE FROM downloads WHERE id = ?", (download_id,))
        await db.commit()
