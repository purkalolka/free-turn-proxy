# Кросс-дистрибутивная обёртка (apt/dnf/yum/apk/pacman/zypper). Best-effort: 0/1.
# Ошибки менеджера пакетов НЕ глотаем: хвост вывода уходит в log() - его видит
# клиент (и живой прогресс), а не молчаливое "install failed". Вывод самих
# пакетников никогда не печатается в stdout: там должен быть только JSON-ответ.

PKG_RETRIES=30      # попыток, пока пакетник занят (блокировка): 30*10с = до 5 минут
PKG_NET_RETRIES=6   # попыток при сетевых сбоях (DNS, зеркало, обрыв)
PKG_RETRY_DELAY=10  # секунд между попытками

pkg_mgr() {
    local m
    for m in apt-get dnf yum apk pacman zypper; do
        if command -v "$m" >/dev/null 2>&1; then echo "$m"; return 0; fi
    done
    return 1
}

# Последние строки вывода пакетника одной строкой (для log).
_pkg_tail() { printf '%s' "${_PKG_OUT:-}" | tail -n 4 | tr '\n' ';' | cut -c1-400; }

# Что за сбой: lock - пакетник занят другим процессом, net - сеть/зеркало,
# пусто - настоящая ошибка (нет такого пакета и т.п.), повтор ей не поможет.
_pkg_class() {  # OUTPUT
    if printf '%s' "$1" | grep -qiE 'could not get lock|unable to acquire the dpkg|unable to lock|is held by process|dpkg was interrupted|another app is currently holding|waiting for process|cannot lock|locked by another'; then
        echo lock; return 0
    fi
    if printf '%s' "$1" | grep -qiE 'temporary failure|could not resolve|connection (timed out|failed|reset|refused)|hash sum mismatch|timed out|timeout was reached|resource temporarily unavailable'; then
        echo net; return 0
    fi
    return 0
}

# _pkg_try CMD... - запуск с повтором на транзиентные сбои. Вывод - в _PKG_OUT,
# код возврата - последней попытки.
_pkg_try() {
    local i=1 rc cls limit
    while :; do
        _PKG_OUT=$("$@" 2>&1) && return 0
        rc=$?
        cls=$(_pkg_class "$_PKG_OUT")
        case "$cls" in
            lock) limit=$PKG_RETRIES ;;
            net)  limit=$PKG_NET_RETRIES ;;
            *)    return "$rc" ;;
        esac
        if [ "$i" -ge "$limit" ]; then return "$rc"; fi
        log "package manager $cls issue (attempt $i/$limit): $(_pkg_tail)"
        # Оборванный dpkg сам не вылечится - install после него не пройдёт никогда.
        if printf '%s' "$_PKG_OUT" | grep -qi 'dpkg was interrupted'; then
            log "repairing interrupted dpkg (dpkg --configure -a)"
            DEBIAN_FRONTEND=noninteractive dpkg --configure -a >/dev/null 2>&1 || true
        fi
        sleep "$PKG_RETRY_DELAY"
        i=$((i + 1))
    done
}

# Свежий VPS: cloud-init в первые минуты держит apt. Ждём его явно (а не гадаем
# по lock-файлам, для которых нужен fuser - его на минимальных образах нет),
# но не дольше 5 минут.
_wait_cloud_init() {
    [ -z "${_CLOUD_INIT_CHECKED:-}" ] || return 0
    _CLOUD_INIT_CHECKED=1
    command -v cloud-init >/dev/null 2>&1 || return 0
    case "$(cloud-init status 2>/dev/null || true)" in
        *running*)
            log "waiting for cloud-init to finish"
            if command -v timeout >/dev/null 2>&1; then
                timeout 300 cloud-init status --wait >/dev/null 2>&1 || true
            fi ;;
    esac
}

# NEEDRESTART_SUSPEND отключает apt-хук needrestart (Ubuntu 22.04+): он молча
# рестартит службы посреди установки и может подвесить неинтерактивный apt.
_apt_env() {
    export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 NEEDRESTART_MODE=l
}

# DPkg::Lock::Timeout (apt >= 1.9.11): apt сам ждёт dpkg-lock, старые версии
# неизвестную опцию просто игнорируют. Lock списков пакетов (apt update) он не
# покрывает - для него повторы в _pkg_try.
_apt() {
    apt-get -o DPkg::Lock::Timeout=60 -o Acquire::Retries=3 \
        -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$@"
}

pkg_install() {  # PKG [PKG...]
    local mgr
    mgr=$(pkg_mgr) || return 1
    case "$mgr" in
        apt-get)
            _apt_env
            _wait_cloud_init
            log "apt: updating package lists"
            if ! _pkg_try _apt update -qq; then
                log "apt update failed, continuing with cached lists: $(_pkg_tail)"
            fi
            log "apt: installing $*"
            _pkg_try _apt install -y -qq "$@" || { log "apt install failed: $(_pkg_tail)"; return 1; } ;;
        dnf)
            log "dnf: installing $*"
            _pkg_try dnf install -y -q "$@" || { log "dnf install failed: $(_pkg_tail)"; return 1; } ;;
        yum)
            log "yum: installing $*"
            _pkg_try yum install -y -q "$@" || { log "yum install failed: $(_pkg_tail)"; return 1; } ;;
        apk)
            log "apk: installing $*"
            _pkg_try apk add --no-cache "$@" || { log "apk add failed: $(_pkg_tail)"; return 1; } ;;
        pacman)
            log "pacman: installing $*"
            _pkg_try pacman -Sy --noconfirm "$@" || { log "pacman install failed: $(_pkg_tail)"; return 1; } ;;
        zypper)
            log "zypper: installing $*"
            _pkg_try zypper --non-interactive install "$@" || { log "zypper install failed: $(_pkg_tail)"; return 1; } ;;
        *)      return 1 ;;
    esac
}

pkg_remove() {  # PKG [PKG...]
    local mgr
    mgr=$(pkg_mgr) || return 1
    case "$mgr" in
        apt-get)
            _apt_env
            _pkg_try _apt remove -y -qq "$@" || { log "apt remove failed: $(_pkg_tail)"; return 1; } ;;
        dnf)    _pkg_try dnf remove -y -q "$@" || return 1 ;;
        yum)    _pkg_try yum remove -y -q "$@" || return 1 ;;
        apk)    _pkg_try apk del "$@" || return 1 ;;
        pacman) _pkg_try pacman -Rns --noconfirm "$@" || return 1 ;;
        zypper) _pkg_try zypper --non-interactive remove "$@" || return 1 ;;
        *)      return 1 ;;
    esac
}
