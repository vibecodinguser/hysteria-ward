#!/bin/sh
# Установщик Hysteria Ward на чистый VPS.
#
# Скрипт рассчитан на Ubuntu LTS и Debian 12/13: он ставит зависимости штатным
# пакетным менеджером, заводит службу systemd, раскладывает каталоги и скачивает
# готовый бинарь из GitHub Releases. Компиляции на сервере нет.
#
# Повторный запуск безопасен: данные, база и сертификаты не пересоздаются, а
# бинарь и юнит заменяются только тогда, когда действительно отличаются.
set -eu

REPO="${HYSTERIA_WARD_REPO:-vibecodinguser/hysteria-ward}"
PRODUCT="hysteria-ward"

BIN="/usr/local/bin/${PRODUCT}"
UNIT="/etc/systemd/system/${PRODUCT}.service"
DROPIN_DIR="/etc/systemd/system/${PRODUCT}.service.d"
DROPIN="${DROPIN_DIR}/bootstrap.conf"
CONF_DIR="/etc/${PRODUCT}"
CERTS_DIR="${CONF_DIR}/certs"
STATE_DIR="/var/lib/${PRODUCT}"
LOG_DIR="/var/log/${PRODUCT}"
DB="${STATE_DIR}/${PRODUCT}.db"
LOG="${LOG_DIR}/${PRODUCT}.log"

SERVICE_USER="${PRODUCT}"
SERVICE_GROUP="${PRODUCT}"

TARGET_VERSION="latest"
# auto — самоподписанный сертификат только пока в каталоге нет ни одного
# сертификата; yes — всегда; no — никогда.
DEBUG_MODE="auto"

usage() {
	cat <<EOF
Установщик ${PRODUCT}.

Использование:
  sh install.sh [--version ВЕРСИЯ] [--debug|--no-debug]

Флаги:
  --version ВЕРСИЯ  версия из GitHub Releases (по умолчанию latest)
  --debug           разрешить самоподписанный сертификат, пока настоящего нет
  --no-debug        не разрешать самоподписанный сертификат
  --help            показать эту справку

Скрипт нужно запускать от root на Ubuntu LTS или Debian 12/13.
EOF
}

info() {
	printf '==> %s\n' "$*"
}

warn() {
	printf 'внимание: %s\n' "$*" >&2
}

die() {
	printf 'ошибка: %s\n' "$*" >&2
	exit 1
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--version)
		[ "$#" -ge 2 ] || die "после --version нужна версия"
		TARGET_VERSION="$2"
		shift 2
		;;
	--version=*)
		TARGET_VERSION="${1#*=}"
		shift
		;;
	--debug)
		DEBUG_MODE="yes"
		shift
		;;
	--no-debug)
		DEBUG_MODE="no"
		shift
		;;
	--help|-h)
		usage
		exit 0
		;;
	*)
		die "неизвестный аргумент: $1 (см. --help)"
		;;
	esac
done

if [ -z "$TARGET_VERSION" ] || [ "$TARGET_VERSION" = "latest" ]; then
	TARGET_VERSION="latest"
else
	printf '%s' "$TARGET_VERSION" | grep -Eq '^(latest|v[0-9][0-9A-Za-z._-]*)$' ||
		die "версия должна выглядеть как v0.1.0 или latest, получено: $TARGET_VERSION"
fi

[ "$(id -u)" -eq 0 ] || die "нужны права root: sudo sh $0"

# --- что за система ---------------------------------------------------------

if [ ! -r /etc/os-release ]; then
	die "не найден /etc/os-release: не удалось определить дистрибутив"
fi

# Читаем поля в подоболочке: /etc/os-release заводит собственные NAME и VERSION,
# а они не должны перетирать переменные установщика.
# shellcheck disable=SC1091
OS_ID="$(. /etc/os-release && printf '%s' "${ID:-}")"
# shellcheck disable=SC1091
OS_VERSION="$(. /etc/os-release && printf '%s' "${VERSION_ID:-}")"
# shellcheck disable=SC1091
OS_NAME="$(. /etc/os-release && printf '%s' "${PRETTY_NAME:-}")"
[ -n "$OS_NAME" ] || OS_NAME="${OS_ID} ${OS_VERSION}"

case "$OS_ID" in
ubuntu)
	case "$OS_VERSION" in
	20.04|22.04|24.04|26.04) ;;
	*) die "Ubuntu $OS_VERSION не поддерживается: нужен LTS 20.04, 22.04, 24.04 или 26.04" ;;
	esac
	;;
debian)
	case "$OS_VERSION" in
	12|12.*|13|13.*) ;;
	*) die "Debian $OS_VERSION не поддерживается: нужны Debian 12 или 13" ;;
	esac
	;;
*)
	die "дистрибутив $OS_NAME не поддерживается: нужны Ubuntu LTS или Debian 12/13"
	;;
esac

case "$(uname -m)" in
x86_64|amd64) ARCH="amd64" ;;
aarch64|arm64) ARCH="arm64" ;;
*) die "архитектура $(uname -m) не поддерживается: нужны x86_64 или aarch64" ;;
esac

[ -d /run/systemd/system ] || die "systemd не запущен: установка рассчитана на systemd-сервер"
command -v systemctl >/dev/null 2>&1 || die "не найден systemctl"

info "система: $OS_NAME, linux/$ARCH"

# --- зависимости ------------------------------------------------------------

MISSING=""
for package in ca-certificates curl tar gzip; do
	if ! dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q "install ok installed"; then
		MISSING="$MISSING $package"
	fi
done

if [ -n "$MISSING" ]; then
	info "ставлю зависимости:$MISSING"
	export DEBIAN_FRONTEND=noninteractive
	apt-get -o DPkg::Lock::Timeout=120 update -qq ||
		die "не удалось обновить список пакетов"
	# Список собран циклом выше; кавычки здесь разбивали бы его на аргументы.
	# shellcheck disable=SC2086
	apt-get -o DPkg::Lock::Timeout=120 install -y --no-install-recommends $MISSING ||
		die "не удалось поставить зависимости"
fi

# --- пользователь и каталоги ------------------------------------------------

if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
	info "завожу группу $SERVICE_GROUP"
	groupadd --system "$SERVICE_GROUP"
fi

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
	info "завожу пользователя $SERVICE_USER"
	useradd --system \
		--gid "$SERVICE_GROUP" \
		--home-dir "$STATE_DIR" \
		--shell /usr/sbin/nologin \
		--no-create-home \
		"$SERVICE_USER"
fi

info "раскладываю каталоги"
install -d -o root -g "$SERVICE_GROUP" -m 0750 "$CONF_DIR"
install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$CERTS_DIR"
install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$STATE_DIR"
install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0750 "$LOG_DIR"

# Базу и журнал мог создать root, например командой passwd до появления службы.
# Владелец должен быть один — тот, от кого работает панель.
if [ -e "$DB" ]; then
	chown "$SERVICE_USER:$SERVICE_GROUP" "$DB"
	chmod 0600 "$DB"
fi
if [ -e "$LOG" ]; then
	chown "$SERVICE_USER:$SERVICE_GROUP" "$LOG"
	chmod 0600 "$LOG"
fi

# --- загрузка релиза --------------------------------------------------------

TMP="$(mktemp -d "${TMPDIR:-/tmp}/hysteria-ward.XXXXXX")" ||
	die "не удалось создать временный каталог"
cleanup() {
	rm -rf "$TMP"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

ASSET="${PRODUCT}_linux_${ARCH}.tar.gz"

if [ "$TARGET_VERSION" = "latest" ]; then
	DEFAULT_BASE="https://github.com/${REPO}/releases/latest/download"
else
	DEFAULT_BASE="https://github.com/${REPO}/releases/download/${TARGET_VERSION}"
fi

# Переменная нужна проверкам установщика: так релиз можно подменить локальным
# каталогом, не трогая настоящий GitHub.
BASE="${HYSTERIA_WARD_BASE_URL:-$DEFAULT_BASE}"

fetch() {
	url="$1"
	out="$2"

	curl --fail --silent --show-error --location \
		--retry 3 --retry-delay 2 --connect-timeout 15 \
		--output "$out" "$url" ||
		die "не удалось скачать $url"
}

info "скачиваю $ASSET ($TARGET_VERSION)"
fetch "$BASE/$ASSET" "$TMP/$ASSET"
fetch "$BASE/checksums.txt" "$TMP/checksums.txt"

EXPECTED="$(awk -v name="$ASSET" '$2 == name { print $1; exit }' "$TMP/checksums.txt")"
[ -n "$EXPECTED" ] || die "в checksums.txt нет строки для $ASSET"

ACTUAL="$(sha256sum "$TMP/$ASSET" | awk '{print $1}')"
[ "$ACTUAL" = "$EXPECTED" ] ||
	die "контрольная сумма $ASSET не сошлась: ожидалась $EXPECTED, получена $ACTUAL"

info "распаковываю"
mkdir -p "$TMP/unpack"
tar -xzf "$TMP/$ASSET" -C "$TMP/unpack" ||
	die "не удалось распаковать $ASSET"

BIN_SRC="$TMP/unpack/$PRODUCT"
UNIT_SRC="$TMP/unpack/$PRODUCT.service"
[ -f "$BIN_SRC" ] || die "в архиве нет $PRODUCT"
[ -f "$UNIT_SRC" ] || die "в архиве нет $PRODUCT.service"

chmod 0755 "$BIN_SRC"

GOT="$("$BIN_SRC" --version)" || die "скачанный бинарь не запускается"
case "$GOT" in
*"linux/$ARCH") ;;
*) die "в архиве бинарь для другой платформы: $GOT" ;;
esac

GOT_VERSION="$(printf '%s\n' "$GOT" | awk '{print $2}')"
[ "$GOT_VERSION" != "dev" ] || die "в релизе оказалась отладочная сборка"
if [ "$TARGET_VERSION" != "latest" ] && [ "$GOT_VERSION" != "$TARGET_VERSION" ]; then
	die "в архиве версия $GOT_VERSION, ожидалась $TARGET_VERSION"
fi

# --- бинарь и юнит ----------------------------------------------------------

same_file() {
	[ -f "$1" ] && [ -f "$2" ] || return 1
	[ "$(sha256sum "$1" | awk '{print $1}')" = "$(sha256sum "$2" | awk '{print $1}')" ]
}

CHANGED=0

if same_file "$BIN_SRC" "$BIN"; then
	info "бинарь уже тот же, оставляю"
else
	info "ставлю бинарь $GOT_VERSION"
	install -m 0755 "$BIN_SRC" "${BIN}.new"
	mv -f "${BIN}.new" "$BIN"
	CHANGED=1
fi

if same_file "$UNIT_SRC" "$UNIT"; then
	info "systemd-юнит уже тот же, оставляю"
else
	info "ставлю systemd-юнит"
	install -m 0644 "$UNIT_SRC" "${UNIT}.new"
	mv -f "${UNIT}.new" "$UNIT"
	CHANGED=1
fi

# До появления настоящего сертификата панель не поднимется: ядро и интерфейс
# читают один и тот же tls. --debug разрешает интерфейсу (и только ему) выписать
# самоподписанный сертификат. Как только в certs/ появится файл, режим выключается:
# самоподписанный сертификат — временная мера на период до ACME, а не рабочий режим.
CERT_FOUND=0
for candidate in "$CERTS_DIR"/*.crt "$CERTS_DIR"/*.pem; do
	[ -f "$candidate" ] || continue

	# Ключи и учётные данные ACME лежат там же, где сертификаты, и сертификатом
	# не являются: годится только имя, в котором есть cert, fullchain или crt.
	case "${candidate##*/}" in
	*cert*|*fullchain*|*crt*)
		CERT_FOUND=1
		break
		;;
	esac
done

case "$DEBUG_MODE" in
yes) USE_DEBUG=1 ;;
no) USE_DEBUG=0 ;;
auto)
	if [ "$CERT_FOUND" -eq 0 ]; then
		USE_DEBUG=1
	else
		USE_DEBUG=0
	fi
	;;
esac

if [ "$USE_DEBUG" -eq 1 ]; then
	install -d -m 0755 "$DROPIN_DIR"
	cat >"$TMP/bootstrap.conf" <<EOF
[Service]
ExecStart=
ExecStart=${BIN} serve --debug
EOF

	if same_file "$TMP/bootstrap.conf" "$DROPIN"; then
		info "временный режим уже включён, оставляю"
	else
		warn "настоящего сертификата ещё нет: панель поднимется с самоподписанным"
		install -m 0644 "$TMP/bootstrap.conf" "$DROPIN"
		CHANGED=1
	fi
else
	if [ -f "$DROPIN" ]; then
		info "настоящий сертификат найден, выключаю временный режим"
		rm -f "$DROPIN"
		CHANGED=1
	fi
	rmdir "$DROPIN_DIR" 2>/dev/null || true
fi

if [ "$CHANGED" -eq 1 ]; then
	systemctl daemon-reload
fi

# --- запуск ----------------------------------------------------------------

systemctl enable "$PRODUCT.service" >/dev/null 2>&1 ||
	die "не удалось включить службу $PRODUCT"

# Прошлая попытка могла упереться в предел перезапусков — тогда systemctl
# откажется запускать, пока счётчик не сброшен.
systemctl reset-failed "$PRODUCT.service" 2>/dev/null || true

if [ "$CHANGED" -eq 1 ]; then
	info "перезапускаю службу"
	systemctl restart "$PRODUCT.service" || true
else
	info "запускаю службу"
	systemctl start "$PRODUCT.service" || true
fi

# Ждём именно active, а не «activating»: процесс, который упал и ждёт
# перезапуска, тоже считается запускающимся, и принять его за работающий —
# значит отрапортовать об успехе поверх отказа.
service_active() {
	[ "$(systemctl show -p ActiveState --value "$PRODUCT.service" 2>/dev/null)" = "active" ]
}

# Даём systemd время поднять процесс, чтобы отказ было видно сразу, а не при
# первом заходе в интерфейс. Одного «active» мало: только что запущенный процесс
# systemd считает работающим ещё мгновение после fork, даже если тот сейчас
# упадёт. Поэтому ждём несколько проверок подряд.
TRIES=0
STREAK=0
while [ "$TRIES" -lt 15 ]; do
	if service_active; then
		STREAK=$((STREAK + 1))
		[ "$STREAK" -ge 3 ] && break
	else
		STREAK=0
	fi
	TRIES=$((TRIES + 1))
	sleep 1
done

if [ "$STREAK" -lt 3 ]; then
	warn "служба не запустилась"
	systemctl status --no-pager --lines=20 "$PRODUCT.service" || true
	journalctl -u "$PRODUCT.service" --no-pager --lines=40 || true
	exit 1
fi

# --- итог ------------------------------------------------------------------

info "готово"
"$BIN" --version

cat <<EOF

Служба:   systemctl status ${PRODUCT}
База:     ${DB}
Журнал:   ${LOG}

Панель по умолчанию слушает только петлю. Открыть её с рабочей машины:
  ssh -L 8443:127.0.0.1:8443 root@<адрес-сервера>
и зайти на https://127.0.0.1:8443

Пароль администратора по умолчанию — admin/admin. Смените его:
  systemctl stop ${PRODUCT}
  runuser -u ${PRODUCT} -- ${BIN} passwd
  systemctl start ${PRODUCT}
EOF

if [ "$USE_DEBUG" -eq 1 ]; then
	cat <<EOF

Сертификата для домена пока нет, поэтому панель поднята с самоподписанным:
браузер предупредит о недоверенном сертификате. Это временный режим до выпуска
сертификата ACME; после появления сертификата в ${CERTS_DIR} повторный запуск
установщика уберёт его.
EOF
fi
