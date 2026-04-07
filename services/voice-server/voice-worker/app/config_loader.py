"""Загрузка конфигурации: YAML (база) + переменные окружения (переопределение)."""

from __future__ import annotations

import os
from pathlib import Path
from typing import Any, Optional

import yaml

from app.config_model import VoiceServerConfig

_GOOGLE_DRIVE_ROOT = Path("/content/drive/MyDrive")
_DEFAULT_DRIVE_DB_DIR = _GOOGLE_DRIVE_ROOT / "mafia-voice"


def _default_package_db_path() -> Path:
    return Path(__file__).resolve().parent.parent / "data" / "voice_registry.sqlite"


def _parse_bool(raw: str) -> bool:
    return raw.strip().lower() in ("1", "true", "yes", "on")


def _threshold_triple(preset: str) -> tuple[float, float, float]:
    p = (preset or "balanced").strip().lower()
    if p == "strict":
        return 0.82, 0.66, 0.38
    if p == "loose":
        return 0.68, 0.54, 0.50
    return 0.75, 0.60, 0.45


def _get_yaml_path() -> Optional[Path]:
    raw = (os.environ.get("VOICE_SERVER_CONFIG_PATH") or os.environ.get("VOICE_SERVER_CONFIG") or "").strip()
    if raw:
        return Path(raw).expanduser()
    candidate = Path(__file__).resolve().parent.parent / "config.yaml"
    if candidate.is_file():
        return candidate
    return None


def _env_float(key: str) -> Optional[float]:
    v = os.environ.get(key)
    if v is None or str(v).strip() == "":
        return None
    return float(v)


def _env_int(key: str) -> Optional[int]:
    v = os.environ.get(key)
    if v is None or str(v).strip() == "":
        return None
    return int(v)


def _load_yaml_dict(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as f:
        data = yaml.safe_load(f)
    if data is None:
        return {}
    if not isinstance(data, dict):
        raise ValueError(f"YAML root must be a mapping, got {type(data).__name__}")
    return {str(k): v for k, v in data.items()}


def _normalize_yaml_value(key: str, value: Any) -> Any:
    """Приводим значения YAML к типам, ожидаемым моделью."""
    if value is None:
        return None
    if key == "device" and isinstance(value, str) and value.strip().lower() in ("", "auto", "none"):
        return None
    return value


def _apply_preset_threshold_defaults(data: dict[str, Any]) -> None:
    preset = str(data.get("voice_threshold_preset") or "balanced")
    bc, bs, bf = _threshold_triple(preset)
    if "threshold_confident_match" not in data:
        data["threshold_confident_match"] = bc
    if "threshold_soft_match" not in data:
        data["threshold_soft_match"] = bs
    if "threshold_force_new" not in data:
        data["threshold_force_new"] = bf


def _overlay_env(data: dict[str, Any]) -> None:
    """Переменные окружения перекрывают значения из YAML."""

    if os.environ.get("VOICE_SERVER_API_KEY") is not None:
        data["api_key"] = os.environ["VOICE_SERVER_API_KEY"]

    v = _env_int("VOICE_SERVER_MAX_FILE_MB")
    if v is not None:
        data["max_file_size_mb"] = v

    if os.environ.get("VOICE_SERVER_DB"):
        data["db_path"] = os.environ["VOICE_SERVER_DB"].strip()

    if os.environ.get("VOICE_SERVER_USE_GOOGLE_DRIVE"):
        data["use_google_drive"] = _parse_bool(os.environ["VOICE_SERVER_USE_GOOGLE_DRIVE"])

    if os.environ.get("VOICE_SERVER_DEVICE"):
        raw = os.environ["VOICE_SERVER_DEVICE"].strip()
        data["device"] = None if raw.lower() in ("", "auto") else raw.lower()

    if os.environ.get("VOICE_SERVER_WHISPER_MODEL"):
        data["whisper_model"] = os.environ["VOICE_SERVER_WHISPER_MODEL"].strip()

    if os.environ.get("HF_TOKEN") is not None:
        data["hf_token"] = os.environ["HF_TOKEN"]

    if os.environ.get("ENABLE_VOICE_LEARNING"):
        data["enable_voice_learning"] = _parse_bool(os.environ["ENABLE_VOICE_LEARNING"])

    if os.environ.get("VOICE_THRESHOLD_PRESET"):
        data["voice_threshold_preset"] = os.environ["VOICE_THRESHOLD_PRESET"].strip().lower()

    for env_key, field, parser in (
        ("THRESHOLD_CONFIDENT_MATCH", "threshold_confident_match", _env_float),
        ("THRESHOLD_SOFT_MATCH", "threshold_soft_match", _env_float),
        ("THRESHOLD_FORCE_NEW", "threshold_force_new", _env_float),
        ("SIMILARITY_UPDATE_MIN", "similarity_update_min", _env_float),
        ("PENDING_MATCH_THRESHOLD", "pending_match_threshold", _env_float),
        ("SPLIT_DISTANCE_THRESHOLD", "split_distance_threshold", _env_float),
        ("CALIBRATION_WINDOW_SEC", "calibration_window", _env_float),
        ("MAX_EXTRA_SLOTS", "max_extra_slots", _env_int),
        ("MIN_SPEAKER_DURATION", "min_speaker_duration", _env_float),
        ("PENDING_MAX_CHUNKS", "pending_max_chunks", _env_int),
        ("EMBEDDING_BUFFER_SIZE", "embedding_buffer_size", _env_int),
        ("MIN_LONG_SEGMENT_SEC", "min_long_segment_sec", _env_float),
    ):
        parsed = parser(env_key)
        if parsed is not None:
            data[field] = parsed

    if os.environ.get("EMBEDDING_BACKEND"):
        data["embedding_backend"] = os.environ["EMBEDDING_BACKEND"].strip()

    for env_key, field in (
        ("HDBSCAN_MIN_CLUSTER_SIZE", "hdbscan_min_cluster_size"),
        ("SPLIT_MAX_CLUSTERS", "split_max_clusters"),
        ("SUB_REBUILD_EVERY", "sub_rebuild_every"),
        ("SEGMENT_EMBEDDINGS_MAX", "segment_embeddings_max"),
        ("MAX_SUB_CENTROIDS", "max_sub_centroids"),
        ("SUB_MIN_CLUSTER_SIZE", "sub_min_cluster_size"),
    ):
        parsed = _env_int(env_key)
        if parsed is not None:
            data[field] = parsed


def load_voice_server_config() -> VoiceServerConfig:
    data: dict[str, Any] = {}

    yaml_path = _get_yaml_path()
    if yaml_path is not None and yaml_path.is_file():
        raw_yaml = _load_yaml_dict(yaml_path)
        for k, v in raw_yaml.items():
            nk = k.replace("-", "_")
            data[nk] = _normalize_yaml_value(nk, v)

    _overlay_env(data)
    _apply_preset_threshold_defaults(data)

    m = VoiceServerConfig
    if hasattr(m, "model_validate"):
        return m.model_validate(data)
    return m.parse_obj(data)


def resolve_database_path(cfg: VoiceServerConfig) -> Path:
    if cfg.db_path:
        return Path(cfg.db_path)
    if cfg.use_google_drive and _GOOGLE_DRIVE_ROOT.is_dir():
        return _DEFAULT_DRIVE_DB_DIR / "voice_registry.sqlite"
    return _default_package_db_path()


def resolve_device(device_override: Optional[str]) -> tuple[str, bool]:
    if device_override and str(device_override).strip():
        dev = str(device_override).strip().lower()
        return dev, dev == "cuda"
    try:
        import torch

        if torch.cuda.is_available():
            return "cuda", True
    except ImportError:
        pass
    return "cpu", False
