"""Публичный API настроек: загрузка из YAML + env, обратная совместимость имён."""

from __future__ import annotations

from pathlib import Path

from app.config_loader import load_voice_server_config, resolve_database_path, resolve_device
from app.config_model import VoiceServerConfig

_cfg: VoiceServerConfig = load_voice_server_config()

# Пути и устройство (вычисляются после загрузки модели)
DATABASE_PATH: Path = resolve_database_path(_cfg)
DEVICE: str
GPU_AVAILABLE: bool
DEVICE, GPU_AVAILABLE = resolve_device(_cfg.device)

# API
API_KEY = _cfg.api_key
MAX_FILE_SIZE_MB = _cfg.max_file_size_mb

# Models
WHISPER_MODEL = _cfg.whisper_model
HF_TOKEN = _cfg.hf_token

ENABLE_VOICE_LEARNING = _cfg.enable_voice_learning

# Пороги и сессия
THRESHOLD_CONFIDENT_MATCH = _cfg.threshold_confident_match
THRESHOLD_SOFT_MATCH = _cfg.threshold_soft_match
THRESHOLD_FORCE_NEW = _cfg.threshold_force_new
SIMILARITY_UPDATE_MIN = _cfg.similarity_update_min
PENDING_MATCH_THRESHOLD = _cfg.pending_match_threshold
SPLIT_DISTANCE_THRESHOLD = _cfg.split_distance_threshold

CALIBRATION_WINDOW = _cfg.calibration_window
MAX_EXTRA_SLOTS = _cfg.max_extra_slots
MIN_SPEAKER_DURATION = _cfg.min_speaker_duration
PENDING_MAX_CHUNKS = _cfg.pending_max_chunks
EMBEDDING_BUFFER_SIZE = _cfg.embedding_buffer_size

MIN_LONG_SEGMENT_SEC = _cfg.min_long_segment_sec

# Реестр / эмбеддинги / профили (раньше только через os.environ)
EMBEDDING_BACKEND = _cfg.embedding_backend
HDBSCAN_MIN_CLUSTER_SIZE = _cfg.hdbscan_min_cluster_size
SPLIT_MAX_CLUSTERS = _cfg.split_max_clusters
SUB_REBUILD_EVERY = _cfg.sub_rebuild_every
SEGMENT_EMBEDDINGS_MAX = _cfg.segment_embeddings_max
MAX_SUB_CENTROIDS = _cfg.max_sub_centroids
SUB_MIN_CLUSTER_SIZE = _cfg.sub_min_cluster_size

__all__ = [
    "VoiceServerConfig",
    "DATABASE_PATH",
    "DEVICE",
    "GPU_AVAILABLE",
    "API_KEY",
    "MAX_FILE_SIZE_MB",
    "WHISPER_MODEL",
    "HF_TOKEN",
    "ENABLE_VOICE_LEARNING",
    "THRESHOLD_CONFIDENT_MATCH",
    "THRESHOLD_SOFT_MATCH",
    "THRESHOLD_FORCE_NEW",
    "SIMILARITY_UPDATE_MIN",
    "PENDING_MATCH_THRESHOLD",
    "SPLIT_DISTANCE_THRESHOLD",
    "CALIBRATION_WINDOW",
    "MAX_EXTRA_SLOTS",
    "MIN_SPEAKER_DURATION",
    "PENDING_MAX_CHUNKS",
    "EMBEDDING_BUFFER_SIZE",
    "MIN_LONG_SEGMENT_SEC",
    "EMBEDDING_BACKEND",
    "HDBSCAN_MIN_CLUSTER_SIZE",
    "SPLIT_MAX_CLUSTERS",
    "SUB_REBUILD_EVERY",
    "SEGMENT_EMBEDDINGS_MAX",
    "MAX_SUB_CENTROIDS",
    "SUB_MIN_CLUSTER_SIZE",
]
