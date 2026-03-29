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

# Пороги: пресет VOICE_THRESHOLD_PRESET=balanced|strict|loose задаёт базу; отдельные THRESHOLD_* переопределяют.


def _env_float(key: str, default: float) -> float:
    v = os.environ.get(key)
    if v is None or str(v).strip() == "":
        return default
    return float(v)


_preset = os.environ.get("VOICE_THRESHOLD_PRESET", "balanced").strip().lower()
_bc, _bs, _bf = 0.75, 0.60, 0.45
if _preset == "strict":
    _bc, _bs, _bf = 0.82, 0.66, 0.38
elif _preset == "loose":
    _bc, _bs, _bf = 0.68, 0.54, 0.50

# balanced / неизвестное значение — дефолты как в whisperx-WavLM-colab
THRESHOLD_CONFIDENT_MATCH = _env_float("THRESHOLD_CONFIDENT_MATCH", _bc)
THRESHOLD_SOFT_MATCH = _env_float("THRESHOLD_SOFT_MATCH", _bs)
THRESHOLD_FORCE_NEW = _env_float("THRESHOLD_FORCE_NEW", _bf)
SIMILARITY_UPDATE_MIN = _env_float("SIMILARITY_UPDATE_MIN", 0.65)
PENDING_MATCH_THRESHOLD = _env_float("PENDING_MATCH_THRESHOLD", 0.55)

CALIBRATION_WINDOW = float(os.environ.get("CALIBRATION_WINDOW_SEC", "300.0"))
MAX_EXTRA_SLOTS = int(os.environ.get("MAX_EXTRA_SLOTS", "2"))
MIN_SPEAKER_DURATION = float(os.environ.get("MIN_SPEAKER_DURATION", "1.0"))
PENDING_MAX_CHUNKS = int(os.environ.get("PENDING_MAX_CHUNKS", "3"))
EMBEDDING_BUFFER_SIZE = int(os.environ.get("EMBEDDING_BUFFER_SIZE", "10"))

# Bootstrap: prefer segments at least this long when ordering (full_file mode)
MIN_LONG_SEGMENT_SEC = float(os.environ.get("MIN_LONG_SEGMENT_SEC", "2.0"))
