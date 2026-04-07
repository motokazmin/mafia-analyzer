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


# Maximum stored embeddings per voice profile (FIFO eviction)
SEGMENT_EMBEDDINGS_MAX = config.SEGMENT_EMBEDDINGS_MAX


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
        conn.execute(
            """
            CREATE TABLE IF NOT EXISTS segment_embeddings (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                voice_id TEXT NOT NULL REFERENCES voices(voice_id) ON DELETE CASCADE,
                session_id TEXT,
                embedding BLOB NOT NULL,
                dim INTEGER NOT NULL,
                created_at REAL NOT NULL
            )
            """
        )
        conn.execute(
            "CREATE INDEX IF NOT EXISTS idx_se_voice_id ON segment_embeddings(voice_id)"
        )
        # ── Новая таблица: суб-центроиды мультидиполя ─────────────────────
        conn.execute(
            """
            CREATE TABLE IF NOT EXISTS voice_sub_centroids (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                voice_id TEXT NOT NULL REFERENCES voices(voice_id) ON DELETE CASCADE,
                idx INTEGER NOT NULL,
                centroid BLOB NOT NULL,
                dim INTEGER NOT NULL,
                updated_at REAL NOT NULL,
                UNIQUE(voice_id, idx)
            )
            """
        )
        conn.execute(
            "CREATE INDEX IF NOT EXISTS idx_vsc_voice_id ON voice_sub_centroids(voice_id)"
        )
        conn.commit()
        # Seed meta keys if missing
        for key, default in [("speaker_counter", "0"), ("session_max_speakers", "")]:
            row = conn.execute(
                "SELECT value FROM meta WHERE key = ?", (key,)
            ).fetchone()
            if row is None:
                conn.execute(
                    "INSERT INTO meta (key, value) VALUES (?, ?)", (key, default)
                )
        self._migrate_voices_schema(conn)
        conn.commit()

    def _connect(self) -> sqlite3.Connection:
        conn = sqlite3.connect(self._path, check_same_thread=False)
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA foreign_keys = ON")
        self._ensure_schema(conn)
        return conn

    # ── speaker counter ────────────────────────────────────────────────────

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

    # ── session_max_speakers persistence ──────────────────────────────────

    def save_session_max_speakers(self, value: Optional[int]) -> None:
        stored = str(value) if value is not None else ""
        with self._lock:
            conn = self._connect()
            try:
                conn.execute(
                    "INSERT INTO meta (key, value) VALUES ('session_max_speakers', ?) "
                    "ON CONFLICT(key) DO UPDATE SET value = excluded.value",
                    (stored,),
                )
                conn.commit()
            finally:
                conn.close()

    def load_session_max_speakers(self) -> Optional[int]:
        with self._lock:
            conn = self._connect()
            try:
                row = conn.execute(
                    "SELECT value FROM meta WHERE key = 'session_max_speakers'"
                ).fetchone()
                if row and row[0]:
                    try:
                        return int(row[0])
                    except ValueError:
                        return None
                return None
            finally:
                conn.close()

    # ── sub_centroids (мультидиполь) ───────────────────────────────────────

    def save_sub_centroids(self, voice_id: str, sub_centroids: list[np.ndarray]) -> None:
        """Сохраняет список суб-центроидов. Перезаписывает все предыдущие."""
        now = time.time()
        with self._lock:
            conn = self._connect()
            try:
                conn.execute(
                    "DELETE FROM voice_sub_centroids WHERE voice_id = ?", (voice_id,)
                )
                for idx, vec in enumerate(sub_centroids):
                    arr = np.asarray(vec, dtype=np.float32).ravel()
                    conn.execute(
                        """
                        INSERT INTO voice_sub_centroids (voice_id, idx, centroid, dim, updated_at)
                        VALUES (?, ?, ?, ?, ?)
                        """,
                        (voice_id, idx, arr.tobytes(), arr.shape[0], now),
                    )
                conn.commit()
            finally:
                conn.close()

    def load_sub_centroids(self, voice_id: str) -> list[np.ndarray]:
        """Загружает суб-центроиды в порядке idx."""
        with self._lock:
            conn = self._connect()
            try:
                rows = conn.execute(
                    "SELECT centroid, dim FROM voice_sub_centroids "
                    "WHERE voice_id = ? ORDER BY idx ASC",
                    (voice_id,),
                ).fetchall()
                result = []
                for row in rows:
                    arr = np.frombuffer(row["centroid"], dtype=np.float32).reshape(int(row["dim"]))
                    result.append(arr.astype(np.float64))
                return result
            finally:
                conn.close()

    def delete_sub_centroids(self, voice_id: str) -> None:
        with self._lock:
            conn = self._connect()
            try:
                conn.execute(
                    "DELETE FROM voice_sub_centroids WHERE voice_id = ?", (voice_id,)
                )
                conn.commit()
            finally:
                conn.close()

    # ── segment_embeddings ─────────────────────────────────────────────────

    def add_segment_embedding(
        self,
        voice_id: str,
        embedding: np.ndarray,
        session_id: Optional[str] = None,
    ) -> None:
        """Store one embedding for a voice profile. Evicts oldest if over limit."""
        emb = np.asarray(embedding, dtype=np.float32).ravel()
        blob = emb.tobytes()
        dim = emb.shape[0]
        now = time.time()
        with self._lock:
            conn = self._connect()
            try:
                conn.execute(
                    """
                    INSERT INTO segment_embeddings (voice_id, session_id, embedding, dim, created_at)
                    VALUES (?, ?, ?, ?, ?)
                    """,
                    (voice_id, session_id, blob, dim, now),
                )
                # Evict oldest rows beyond SEGMENT_EMBEDDINGS_MAX for this voice
                conn.execute(
                    """
                    DELETE FROM segment_embeddings
                    WHERE voice_id = ? AND id NOT IN (
                        SELECT id FROM segment_embeddings
                        WHERE voice_id = ?
                        ORDER BY created_at DESC
                        LIMIT ?
                    )
                    """,
                    (voice_id, voice_id, SEGMENT_EMBEDDINGS_MAX),
                )
                conn.commit()
            finally:
                conn.close()

    def get_segment_embeddings(self, voice_id: str) -> list[np.ndarray]:
        """Return all stored embeddings for a voice profile (oldest first)."""
        with self._lock:
            conn = self._connect()
            try:
                rows = conn.execute(
                    "SELECT embedding, dim FROM segment_embeddings WHERE voice_id = ? ORDER BY created_at ASC",
                    (voice_id,),
                ).fetchall()
                result = []
                for row in rows:
                    arr = np.frombuffer(row["embedding"], dtype=np.float32).reshape(int(row["dim"]))
                    result.append(arr)
                return result
            finally:
                conn.close()

    def get_segment_embeddings_count(self, voice_id: str) -> int:
        with self._lock:
            conn = self._connect()
            try:
                row = conn.execute(
                    "SELECT COUNT(*) FROM segment_embeddings WHERE voice_id = ?",
                    (voice_id,),
                ).fetchone()
                return int(row[0]) if row else 0
            finally:
                conn.close()

    def reassign_segment_embeddings(self, source_id: str, target_id: str) -> int:
        """Move all segment embeddings from source to target (used in merge)."""
        with self._lock:
            conn = self._connect()
            try:
                cur = conn.execute(
                    "UPDATE segment_embeddings SET voice_id = ? WHERE voice_id = ?",
                    (target_id, source_id),
                )
                conn.commit()
                return cur.rowcount
            finally:
                conn.close()

    def delete_segment_embeddings(self, voice_id: str) -> None:
        with self._lock:
            conn = self._connect()
            try:
                conn.execute(
                    "DELETE FROM segment_embeddings WHERE voice_id = ?", (voice_id,)
                )
                conn.commit()
            finally:
                conn.close()

    def split_segment_embeddings(
        self,
        source_id: str,
        new_id: str,
        indices_for_new: list[int],
    ) -> None:
        """
        After clustering, reassign a subset of segment_embeddings rows to a new voice.
        indices_for_new: 0-based positions (oldest-first) to move to new_id.
        """
        with self._lock:
            conn = self._connect()
            try:
                rows = conn.execute(
                    "SELECT id FROM segment_embeddings WHERE voice_id = ? ORDER BY created_at ASC",
                    (source_id,),
                ).fetchall()
                ids_to_move = [rows[i]["id"] for i in indices_for_new if i < len(rows)]
                if ids_to_move:
                    placeholders = ",".join("?" * len(ids_to_move))
                    conn.execute(
                        f"UPDATE segment_embeddings SET voice_id = ? WHERE id IN ({placeholders})",
                        [new_id] + ids_to_move,
                    )
                conn.commit()
            finally:
                conn.close()

    # ── voices CRUD ────────────────────────────────────────────────────────

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
        """Удалить все голоса, обнулить счётчики, session_max_speakers и segment_embeddings."""
        with self._lock:
            conn = self._connect()
            try:
                conn.execute("DELETE FROM segment_embeddings")
                conn.execute("DELETE FROM voice_sub_centroids")
                conn.execute("DELETE FROM voices")
                conn.execute(
                    "UPDATE meta SET value = '0' WHERE key = 'speaker_counter'"
                )
                conn.execute(
                    "UPDATE meta SET value = '' WHERE key = 'session_max_speakers'"
                )
                conn.commit()
            finally:
                conn.close()

    def delete_voice(self, voice_id: str) -> bool:
        with self._lock:
            conn = self._connect()
            try:
                # segment_embeddings и voice_sub_centroids удаляются каскадно
                cur = conn.execute("DELETE FROM voices WHERE voice_id = ?", (voice_id,))
                conn.commit()
                return cur.rowcount > 0
            finally:
                conn.close()

    def merge_voices(self, source_id: str, target_id: str) -> bool:
        """Merge source into target using segment-count weighted average. Reassigns embeddings."""
        s = self.get_voice(source_id)
        t = self.get_voice(target_id)
        if s is None or t is None:
            return False

        total_seg = s.segment_count + t.segment_count
        if total_seg > 0:
            new_c = (
                s.centroid.astype(np.float64) * s.segment_count
                + t.centroid.astype(np.float64) * t.segment_count
            ) / total_seg
        else:
            new_c = (s.centroid.astype(np.float64) + t.centroid.astype(np.float64)) / 2.0

        n = float(np.linalg.norm(new_c))
        if n > 1e-8:
            new_c = new_c / n

        blob = np.asarray(new_c, dtype=np.float32).tobytes()
        dim = new_c.shape[0]
        now = time.time()
        with self._lock:
            conn = self._connect()
            try:
                # Reassign segment_embeddings before deleting source
                conn.execute(
                    "UPDATE segment_embeddings SET voice_id = ? WHERE voice_id = ?",
                    (target_id, source_id),
                )
                # Удаляем суб-центроиды обоих — registry пересчитает после merge
                conn.execute(
                    "DELETE FROM voice_sub_centroids WHERE voice_id IN (?, ?)",
                    (source_id, target_id),
                )
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