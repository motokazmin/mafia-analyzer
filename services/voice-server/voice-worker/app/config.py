import os
from pathlib import Path

# API
API_KEY = os.environ.get("VOICE_SERVER_API_KEY", "barchik")
MAX_FILE_SIZE_MB = int(os.environ.get("VOICE_SERVER_MAX_FILE_MB", "500"))

# Paths
_DEFAULT_DB = Path(__file__).resolve().parent.parent / "data" / "voice_registry.sqlite"
_GOOGLE_DRIVE_ROOT = Path("/content/drive/MyDrive")
_DEFAULT_DRIVE_DB_DIR = _GOOGLE_DRIVE_ROOT / "mafia-voice"


def _resolve_database_path() -> Path:
    if os.environ.get("VOICE_SERVER_DB"):
        return Path(os.environ["VOICE_SERVER_DB"])
    use_drive = os.environ.get("VOICE_SERVER_USE_GOOGLE_DRIVE", "").lower() in (
        "1",
        "true",
        "yes",
    )
    if use_drive and _GOOGLE_DRIVE_ROOT.is_dir():
        return _DEFAULT_DRIVE_DB_DIR / "voice_registry.sqlite"
    return _DEFAULT_DB


DATABASE_PATH = _resolve_database_path()

# Models
DEVICE = os.environ.get("VOICE_SERVER_DEVICE", "cuda")
WHISPER_MODEL = os.environ.get("VOICE_SERVER_WHISPER_MODEL", "large-v2")
HF_TOKEN = os.environ.get("HF_TOKEN", "")

# Continual learning: update centroids on confident matches (streaming + full_file bootstrap)
ENABLE_VOICE_LEARNING = os.environ.get("ENABLE_VOICE_LEARNING", "true").lower() in (
    "1",
    "true",
    "yes",
)

# Thresholds (same semantics as whisperx-WavLM-colab notebook)
THRESHOLD_CONFIDENT_MATCH = float(os.environ.get("THRESHOLD_CONFIDENT_MATCH", "0.75"))
THRESHOLD_SOFT_MATCH = float(os.environ.get("THRESHOLD_SOFT_MATCH", "0.60"))
THRESHOLD_FORCE_NEW = float(os.environ.get("THRESHOLD_FORCE_NEW", "0.45"))
SIMILARITY_UPDATE_MIN = float(os.environ.get("SIMILARITY_UPDATE_MIN", "0.65"))
PENDING_MATCH_THRESHOLD = float(os.environ.get("PENDING_MATCH_THRESHOLD", "0.55"))

CALIBRATION_WINDOW = float(os.environ.get("CALIBRATION_WINDOW_SEC", "300.0"))
MAX_EXTRA_SLOTS = int(os.environ.get("MAX_EXTRA_SLOTS", "2"))
MIN_SPEAKER_DURATION = float(os.environ.get("MIN_SPEAKER_DURATION", "1.0"))
PENDING_MAX_CHUNKS = int(os.environ.get("PENDING_MAX_CHUNKS", "3"))
EMBEDDING_BUFFER_SIZE = int(os.environ.get("EMBEDDING_BUFFER_SIZE", "10"))

# Bootstrap: prefer segments at least this long when ordering (full_file mode)
MIN_LONG_SEGMENT_SEC = float(os.environ.get("MIN_LONG_SEGMENT_SEC", "2.0"))
