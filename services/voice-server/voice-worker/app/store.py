"""SQLite persistence for voice centroids and display names."""

from __future__ import annotations

import sqlite3
import threading
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Optional

import numpy as np

from app import config


@dataclass
class VoiceRow:
    voice_id: str
    display_name: Optional[str]
    centroid: np.ndarray
    segment_count: int
    created_at: float
    updated_at: float
    unreliable: bool = False


def _ensure_parent(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)


class VoiceStore:
    """Thread-safe SQLite store for speaker embeddings."""

    def __init__(self, db_path: Path | None = None) -> None:
        self._path = Path(db_path or config.DATABASE_PATH)
        _ensure_parent(self._path)
        self._lock = threading.Lock()
        with self._lock:
            conn = self._connect()
            conn.close()

    def _migrate_voices_schema(self, conn: sqlite3.Connection) -> None:
        cur = conn.execute(
            "SELECT 1 FROM sqlite_master WHERE type='table' AND name='voices'"
        )
        if cur.fetchone() is None:
            return
        cur = conn.execute("PRAGMA table_info(voices)")
        cols = {str(r[1]) for r in cur.fetchall()}
        if "flag_unreliable" not in cols:
            conn.execute(
                "ALTER TABLE voices ADD COLUMN flag_unreliable INTEGER NOT NULL DEFAULT 0"
            )

    def _ensure_schema(self, conn: sqlite3.Connection) -> None:
        """Idempotent DDL: безопасно после ручной чистки/подмены файла БД."""
        # executescript() выдаёт неявный COMMIT в начале и запускает DDL вне
        # стандартного управления транзакциями Python sqlite3.
        # На Google Drive (Colab) это приводит к тому, что CREATE TABLE не
        # попадает в WAL до следующей операции — отсюда "no such table: voices".
        # Решение: обычные execute() + явный commit().
        conn.execute(
            """
            CREATE TABLE IF NOT EXISTS meta (
                key TEXT PRIMARY KEY,
                value TEXT NOT NULL
            )
            """
        )
        conn.execute(
            """
            CREATE TABLE IF NOT EXISTS voices (
                voice_id TEXT PRIMARY KEY,
                display_name TEXT,
                centroid BLOB NOT NULL,
                dim INTEGER NOT NULL,
                segment_count INTEGER NOT NULL DEFAULT 0,
                created_at REAL NOT NULL,
                updated_at REAL NOT NULL
            )
            """
        )
        conn.commit()
        row = conn.execute(
            "SELECT value FROM meta WHERE key = 'speaker_counter'"
        ).fetchone()
        if row is None:
            conn.execute(
                "INSERT INTO meta (key, value) VALUES ('speaker_counter', '0')"
            )
        self._migrate_voices_schema(conn)
        conn.commit()

    def _connect(self) -> sqlite3.Connection:
        conn = sqlite3.connect(self._path, check_same_thread=False)
        conn.row_factory = sqlite3.Row
        self._ensure_schema(conn)
        return conn

    def get_speaker_counter(self) -> int:
        with self._lock:
            conn = self._connect()
            try:
                row = conn.execute(
                    "SELECT value FROM meta WHERE key = 'speaker_counter'"
                ).fetchone()
                return int(row[0]) if row else 0
            finally:
                conn.close()

    def increment_speaker_counter(self) -> int:
        with self._lock:
            conn = self._connect()
            try:
                cur = int(
                    conn.execute(
                        "SELECT value FROM meta WHERE key = 'speaker_counter'"
                    ).fetchone()[0]
                )
                cur += 1
                conn.execute(
                    "UPDATE meta SET value = ? WHERE key = 'speaker_counter'",
                    (str(cur),),
                )
                conn.commit()
                return cur
            finally:
                conn.close()

    def insert_voice(
        self,
        centroid: np.ndarray,
        display_name: Optional[str] = None,
        voice_id: Optional[str] = None,
    ) -> str:
        vid = voice_id or str(uuid.uuid4())
        centroid = np.asarray(centroid, dtype=np.float32).ravel()
        blob = centroid.tobytes()
        dim = centroid.shape[0]
        now = time.time()
        with self._lock:
            conn = self._connect()
            try:
                conn.execute(
                    """
                    INSERT INTO voices (voice_id, display_name, centroid, dim, segment_count, created_at, updated_at)
                    VALUES (?, ?, ?, ?, 1, ?, ?)
                    """,
                    (vid, display_name, blob, dim, now, now),
                )
                conn.commit()
            finally:
                conn.close()
        return vid

    def update_voice(
        self,
        voice_id: str,
        centroid: np.ndarray,
        segment_count_delta: int = 0,
        display_name: Any = None,
    ) -> None:
        centroid = np.asarray(centroid, dtype=np.float32).ravel()
        blob = centroid.tobytes()
        dim = centroid.shape[0]
        now = time.time()
        with self._lock:
            conn = self._connect()
            try:
                if display_name is not None:
                    conn.execute(
                        """
                        UPDATE voices SET centroid = ?, dim = ?, segment_count = segment_count + ?,
                        display_name = ?, updated_at = ?
                        WHERE voice_id = ?
                        """,
                        (blob, dim, segment_count_delta, display_name, now, voice_id),
                    )
                else:
                    conn.execute(
                        """
                        UPDATE voices SET centroid = ?, dim = ?, segment_count = segment_count + ?,
                        updated_at = ?
                        WHERE voice_id = ?
                        """,
                        (blob, dim, segment_count_delta, now, voice_id),
                    )
                conn.commit()
            finally:
                conn.close()

    def set_display_name(self, voice_id: str, display_name: str) -> bool:
        now = time.time()
        with self._lock:
            conn = self._connect()
            try:
                cur = conn.execute(
                    "UPDATE voices SET display_name = ?, updated_at = ? WHERE voice_id = ?",
                    (display_name, now, voice_id),
                )
                conn.commit()
                return cur.rowcount > 0
            finally:
                conn.close()

    def get_voice(self, voice_id: str) -> Optional[VoiceRow]:
        with self._lock:
            conn = self._connect()
            try:
                row = conn.execute(
                    "SELECT * FROM voices WHERE voice_id = ?", (voice_id,)
                ).fetchone()
                if row is None:
                    return None
                return self._row_to_voice(row)
            finally:
                conn.close()

    def list_voices(self) -> list[VoiceRow]:
        with self._lock:
            conn = self._connect()
            try:
                rows = conn.execute(
                    "SELECT * FROM voices ORDER BY updated_at DESC"
                ).fetchall()
                return [self._row_to_voice(r) for r in rows]
            finally:
                conn.close()

    def _row_to_voice(self, row: sqlite3.Row) -> VoiceRow:
        blob = row["centroid"]
        dim = int(row["dim"])
        centroid = np.frombuffer(blob, dtype=np.float32).reshape(dim)
        ur = False
        try:
            ur = bool(row["flag_unreliable"])
        except (KeyError, IndexError, ValueError):
            pass
        return VoiceRow(
            voice_id=row["voice_id"],
            display_name=row["display_name"],
            centroid=centroid,
            segment_count=int(row["segment_count"]),
            created_at=float(row["created_at"]),
            updated_at=float(row["updated_at"]),
            unreliable=ur,
        )

    def set_flag_unreliable(self, voice_id: str, unreliable: bool) -> bool:
        now = time.time()
        with self._lock:
            conn = self._connect()
            try:
                cur = conn.execute(
                    "UPDATE voices SET flag_unreliable = ?, updated_at = ? WHERE voice_id = ?",
                    (1 if unreliable else 0, now, voice_id),
                )
                conn.commit()
                return cur.rowcount > 0
            finally:
                conn.close()

    def wipe_all(self) -> None:
        """Удалить все голоса и обнулить счётчик Игрок_N."""
        with self._lock:
            conn = self._connect()
            try:
                conn.execute("DELETE FROM voices")
                conn.execute(
                    "UPDATE meta SET value = '0' WHERE key = 'speaker_counter'"
                )
                conn.commit()
            finally:
                conn.close()

    def delete_voice(self, voice_id: str) -> bool:
        with self._lock:
            conn = self._connect()
            try:
                cur = conn.execute("DELETE FROM voices WHERE voice_id = ?", (voice_id,))
                conn.commit()
                return cur.rowcount > 0
            finally:
                conn.close()

    def merge_voices(self, source_id: str, target_id: str) -> bool:
        # Average centroids, sum segment counts, delete source.
        s = self.get_voice(source_id)
        t = self.get_voice(target_id)
        if s is None or t is None:
            return False
        new_c = (s.centroid + t.centroid) / 2.0
        n = float(np.linalg.norm(new_c))
        if n > 1e-8:
            new_c = new_c / n
        total_seg = s.segment_count + t.segment_count
        now = time.time()
        blob = np.asarray(new_c, dtype=np.float32).tobytes()
        dim = new_c.shape[0]
        with self._lock:
            conn = self._connect()
            try:
                conn.execute(
                    """
                    UPDATE voices SET centroid = ?, dim = ?, segment_count = ?, updated_at = ?
                    WHERE voice_id = ?
                    """,
                    (blob, dim, total_seg, now, target_id),
                )
                conn.execute("DELETE FROM voices WHERE voice_id = ?", (source_id,))
                conn.commit()
            finally:
                conn.close()
        return True