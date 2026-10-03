# Загрузки с GitHub. Канал VPS->GitHub у многих хостеров плохой (throttling,
# обрывы): без повторов установка падала на первом же сбое, а голый --max-time
# убивал медленную, но идущую загрузку. Поэтому: повторы, таймауты на подключение
# и обрыв по ЗАВИСАНИЮ потока (а не по общему времени).

# curl в разных версиях знает разные флаги (CentOS 7: 7.29) - неизвестный флаг
# убивает вызов целиком, поэтому проверяем по справке. Справка - в переменную, не
# в конвейер: grep -q + pipefail дал бы ложный отказ из-за SIGPIPE у curl.
_curl_has() {  # FLAG
    local h
    h=$({ curl --help all 2>/dev/null || curl --help 2>/dev/null; } || true)
    case "$h" in *"$1"*) return 0 ;; esac
    return 1
}

_CURL_RETRY=()
_curl_retry_init() {
    [ "${#_CURL_RETRY[@]}" -eq 0 ] || return 0
    _CURL_RETRY=(--retry 5 --retry-delay 3)
    if _curl_has '--retry-connrefused'; then _CURL_RETRY+=(--retry-connrefused); fi
    if _curl_has '--retry-all-errors'; then _CURL_RETRY+=(--retry-all-errors); fi
}

# Быстрая проверка, что до GitHub вообще можно достучаться (3 попытки по <=10с).
# Без неё недоступный GitHub (таймауты, а не быстрый отказ) держал бы установку
# минутами на повторах curl, прежде чем клиент смог бы переключиться на запасной
# путь - заливку бинарника с компьютера. Любой HTTP-ответ считается "доступен".
_github_reachable() {
    local i
    for i in 1 2 3; do
        if command -v curl >/dev/null 2>&1; then
            curl -sI --connect-timeout 5 --max-time 10 -o /dev/null "$RELEASES_URL" 2>/dev/null && return 0
        elif command -v wget >/dev/null 2>&1; then
            wget -q --spider --timeout=8 --tries=1 "$RELEASES_URL" 2>/dev/null && return 0
        else
            return 0   # нечем проверять - пусть решает сама загрузка
        fi
        sleep 1
    done
    return 1
}

# Причина последнего провала _dl (текст ошибки curl/wget) и его код возврата.
_DL_LASTERR=""
_DL_RC=0

# -sS: без прогресс-метра (он ушёл бы в stderr, а тот у клиента сливается со
# stdout), но с текстом ошибки. Ошибку не печатаем, а сохраняем.
_dl() {  # URL OUT
    local url=$1 out=$2
    if command -v curl >/dev/null 2>&1; then
        _curl_retry_init
        _DL_LASTERR=$(curl -fsSL --connect-timeout 15 "${_CURL_RETRY[@]}" \
            --speed-limit 1024 --speed-time 60 --max-time 900 \
            -o "$out" "$url" 2>&1) && { _DL_RC=0; return 0; }
        _DL_RC=$?
    elif command -v wget >/dev/null 2>&1; then
        _DL_LASTERR=$(wget -q --tries=5 --waitretry=3 --timeout=30 -O "$out" "$url" 2>&1) && { _DL_RC=0; return 0; }
        _DL_RC=$?
    else
        fail download_failed "neither curl nor wget present"
    fi
    return 1
}

# Ответ сервера с кодом ошибки (404...), а не сбой сети: curl 22, wget 8.
_dl_http_error() { [ "$_DL_RC" = 22 ] || [ "$_DL_RC" = 8 ]; }

# Небольшой текст (checksums.txt, ответ API) в stdout; код 1 - не получилось.
_fetch_text() {  # URL
    if command -v curl >/dev/null 2>&1; then
        _curl_retry_init
        curl -fsSL --connect-timeout 15 --max-time 60 "${_CURL_RETRY[@]}" "$1" 2>/dev/null
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- --timeout=30 --tries=3 "$1" 2>/dev/null
    else
        return 1
    fi
}

# Тег из ответа API. Запасной путь: api.github.com отдаёт лимит 60 запросов/час на
# IP, поэтому основной способ - редирект (см. _resolve_version).
_resolve_version_api() {
    local body
    body=$(_fetch_text "$API_LATEST_URL") || return 0
    # || true: под pipefail любой сбой конвейера убил бы весь скрипт молча.
    printf '%s' "$body" | sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' | head -n1 || true
}

# GitHub latest/download/<asset> отвечает 302 с Location, где тег vX.Y.Z. Берём из URL.
_resolve_version() {  # URL -> tag (или пусто)
    local url=$1 loc="" tag=""
    if command -v curl >/dev/null 2>&1; then
        _curl_retry_init
        loc=$(curl -sI --connect-timeout 15 --max-time 30 "${_CURL_RETRY[@]}" "$url" 2>/dev/null \
            | awk -F': ' 'tolower($1)=="location"{print $2}' | tr -d '\r' | head -n1) || true
    elif command -v wget >/dev/null 2>&1; then
        loc=$(wget --spider --server-response --timeout=30 --tries=3 "$url" 2>&1 \
            | awk '/[Ll]ocation:/{print $2}' | tr -d '\r' | head -n1) || true
    fi
    tag=$(printf '%s' "$loc" | sed -nE 's#.*/releases/download/([^/]+)/.*#\1#p')
    if [ -z "$tag" ]; then tag=$(_resolve_version_api); fi
    printf '%s' "$tag"
}

# sha256 ассета из checksums.txt того же релиза. Best-effort: нет файла (старый
# релиз, сбой сети) - просто не проверяем, как и раньше.
_release_sha() {  # TAG ASSET -> sha256 или пусто
    local sums
    sums=$(_fetch_text "$RELEASES_URL/download/$1/checksums.txt") || return 0
    printf '%s\n' "$sums" | awk -v a="$2" '$2==a {print $1; exit}' || true
}

# Проверка скачанного: размер + ELF-magic (+ опц. sha256). Ловит усечённые и
# подменённые загрузки, после которых сервис иначе падал бы непонятно почему.
# 0 - ок; 1 - плохо (файл удалён, причина в _DL_ERR).
_DL_ERR=""
_check_download() {  # FILE [EXPECTED_SHA]
    local f=$1 want=${2:-} size magic got
    size=$(wc -c < "$f" 2>/dev/null || echo 0)
    if [ "$size" -lt 100000 ]; then
        rm -f "$f"; _DL_ERR="too_small:$size"; return 1
    fi
    # od может отсутствовать (busybox) - тогда ELF-чек пропускаем, не валим.
    magic=$(od -An -tx1 -N4 "$f" 2>/dev/null | tr -d ' \n') || magic=""
    if [ -n "$magic" ] && [ "$magic" != "7f454c46" ]; then
        rm -f "$f"; _DL_ERR="not_elf"; return 1
    fi
    if [ -n "$want" ] && command -v sha256sum >/dev/null 2>&1; then
        got=$(sha256sum "$f" | awk '{print $1}')
        if [ -n "$got" ] && [ "$got" != "$want" ]; then
            rm -f "$f"; _DL_ERR="sha_mismatch:$got"; return 1
        fi
    fi
    return 0
}

# То же, но с fail при провале (для файла, залитого клиентом: повторять нечего).
_verify_download() {  # FILE [EXPECTED_SHA]
    if _check_download "$@"; then return 0; fi
    case "$_DL_ERR" in
        too_small*)    fail too_small "downloaded file too small (${_DL_ERR#*:} bytes)" ;;
        not_elf)       fail not_elf "downloaded file is not an ELF binary" ;;
        sha_mismatch*) fail sha_mismatch "sha256 mismatch (got ${_DL_ERR#*:})" ;;
    esac
    fail download_failed "verification failed"
}

# _fetch_verified ASSET_URL LATEST_URL OUT TAG NAME - скачать и проверить, до 3
# раз (сверх повторов самого curl). fail download_failed, если так и не вышло.
_fetch_verified() {
    local asset_url=$1 latest_url=$2 out=$3 tag=$4 name=$5 want="${ARG_SHA256:-}" attempt=1
    if [ -z "$want" ]; then want=$(_release_sha "$tag" "$name"); fi
    if ! [[ "$want" =~ ^[0-9a-fA-F]{64}$ ]]; then want=""; fi
    [ -n "$want" ] || log "no sha256 published for $name @ $tag; skipping checksum"
    while [ "$attempt" -le 3 ]; do
        # Качаем по тегу (не latest): релиз мог переехать между резолвом и загрузкой.
        # К latest идём только если тег ответил ошибкой HTTP: при сбое сети тот же
        # хост не ответит и там.
        local got=0
        if _dl "$asset_url" "$out"; then
            got=1
        elif _dl_http_error && _dl "$latest_url" "$out"; then
            got=1
        fi
        if [ "$got" = 1 ]; then
            if _check_download "$out" "$want"; then return 0; fi
            log "download failed verification (attempt $attempt/3): $_DL_ERR"
        else
            # Сеть: curl уже повторил 5 раз - внешние повторы только тянут время,
            # клиенту пора переключаться на заливку с компьютера.
            log "download failed: $(printf '%s' "$_DL_LASTERR" | tail -n 2 | tr '\n' ';' | cut -c1-300)"
            break
        fi
        attempt=$((attempt + 1))
        if [ "$attempt" -le 3 ]; then sleep 3; fi
    done
    rm -f "$out"
    fail download_failed "binary download failed after retries: $(printf '%s' "${_DL_LASTERR:-$_DL_ERR}" | tail -n 1 | cut -c1-300)"
}
