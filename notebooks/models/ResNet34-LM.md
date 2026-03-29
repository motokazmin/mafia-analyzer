# wespeaker-ResNet34-LM — pyannote/wespeaker-voxceleb-resnet34-LM

## Общее

| Параметр | Значение |
|---|---|
| Размер модели | ~26 MB |
| Архитектура | ResNet34 с Large Margin softmax loss |
| Обучена на | VoxCeleb1 + VoxCeleb2 через WeSpeaker фреймворк |
| Диапазон сходства (один человек) | 0.42–0.60 в шумных условиях |
| Диапазон сходства (разные люди) | 0.09–0.50 в шумных условиях |

## Ключевое отличие от ECAPA

Large Margin loss специально обучает модель **разводить разных людей дальше** друг
от друга в пространстве эмбеддингов. В чистых условиях это работает хорошо.
Однако в шумной комнате с одним микрофоном преимущество нивелируется — диапазоны
сходства схожи с ECAPA и так же перекрываются.

Модель легче ECAPA в 3.5 раза, что ускоряет загрузку и занимает меньше памяти GPU.

## Рекомендуемые константы

### Python

```python
SIMILARITY_THRESHOLD_CALIBRATION = 0.45   # строгий при заполнении реестра
SIMILARITY_THRESHOLD_MATCHED     = 0.42   # мягкий после заполнения
SIMILARITY_UPDATE_MIN            = 0.50   # минимум для обновления центроида
MIN_SPEAKER_DURATION             = 1.5    # сек, минимум голоса для эмбеддинга
EMBEDDING_BUFFER_SIZE            = 10     # последних эмбеддингов на спикера
```

> Пороги идентичны ECAPA — реальные значения сходства в шумных условиях оказались
> схожими. В чистых условиях можно попробовать поднять до 0.65/0.55.

### Go

```go
chunkDuration = 30    // сек
overlapSec    = 10    // сек
audioFilter   = "highpass=f=300,lowpass=f=3400,dynaudnorm=p=0.9"
```

## Настройка под стиль игры

### Тихая комната, хороший микрофон
```python
# Здесь ResNet34 может показать преимущество перед ECAPA
SIMILARITY_THRESHOLD_CALIBRATION = 0.70
SIMILARITY_THRESHOLD_MATCHED     = 0.60
SIMILARITY_UPDATE_MIN            = 0.68
```

### Шумная комната, один микрофон (текущий случай)
```python
SIMILARITY_THRESHOLD_CALIBRATION = 0.45
SIMILARITY_THRESHOLD_MATCHED     = 0.42
SIMILARITY_UPDATE_MIN            = 0.50
```

### Короткие реплики, игроки часто перебивают
```python
MIN_SPEAKER_DURATION = 0.8
```
```go
overlapSec = 15
```

### Длинные монологи, спокойная игра
```python
MIN_SPEAKER_DURATION = 2.5
EMBEDDING_BUFFER_SIZE = 20
```

### Постобработка записи (задержка не важна)
```go
chunkDuration = 120
overlapSec    = 20
```

## Признаки что пороги выставлены верно

- Сходство при регистрации нового игрока: `< 0.40`
- Каждый `SPEAKER_XX` ходит ровно в одного `Игрок_N`
- После заполнения реестра нет новых регистраций

## Признаки проблем и решения

| Симптом в логе | Причина | Решение |
|---|---|---|
| `SPEAKER_00 → Игрок_2` и `SPEAKER_01 → Игрок_2` | CALIBRATION слишком низкий | подними на 0.05 |
| Новый игрок с сходством 0.55+ | CALIBRATION слишком высокий | опусти на 0.05 |
| `недостаточно голоса` часто | MIN_SPEAKER_DURATION велик | понизь до 1.0 |
| После реестра спикеры скачут | MATCHED слишком высокий | опусти на 0.05 |

## Загрузка в коде

```python
from pyannote.audio import Model as PyannoteModel, Inference

_emb_base = PyannoteModel.from_pretrained(
    "pyannote/wespeaker-voxceleb-resnet34-LM",
    use_auth_token=HF_TOKEN
)
embedding_model = Inference(_emb_base, window="whole")
embedding_model.to(torch.device(DEVICE))

# Извлечение эмбеддинга
waveform = torch.tensor(combined).unsqueeze(0).float()
emb = embedding_model({"waveform": waveform, "sample_rate": sample_rate})
return np.array(emb)
```

## Когда выбирать эту модель вместо ECAPA

- Мало памяти GPU (экономит ~65 MB)
- Чистые условия записи — Large Margin loss даёт лучшее разделение
- Уже используешь pyannote экосистему — единый стек, проще поддерживать

## Когда вернуться на ECAPA

- Шумная комната с одним микрофоном — разница незначительна,
  а ECAPA чуть стабильнее на коротких отрезках по наблюдениям
