#!/bin/sh
# Установщик Hysteria Ward на чистый VPS.
#
# Скрипт рассчитан на Ubuntu LTS и Debian 12/13: он ставит зависимости штатным
# пакетным менеджером, заводит службу systemd, раскладывает каталоги и скачивает
# готовый бинарь из GitHub Releases. Компиляции на сервере нет.
#
# Установка спрашивает то, что нельзя угадать: домен, почту для ACME, режим
# доступа к панели, masquerade и пароль администратора. Без терминала вопросы не
# задаются: все ответы должны прийти флагами, иначе скрипт сразу говорит, чего не
# хватает, вместо того чтобы ждать ввода, которого не будет.
#
# Повторный запуск безопасен: данные, база и сертификаты не пересоздаются, а
# бинарь и юнит заменяются только тогда, когда действительно отличаются. Ответы
# конфигуратора применяются только к новой базе: настроенную панель повторный
# запуск не переписывает.
set -eu

REPO="${HYSTERIA_WARD_REPO:-vibecodinguser/hysteria-ward}"
PRODUCT="hysteria-ward"

BIN="/usr/local/bin/${PRODUCT}"
UNIT="/etc/systemd/system/${PRODUCT}.service"
DROPIN_DIR="/etc/systemd/system/${PRODUCT}.service.d"
DROPIN="${DROPIN_DIR}/bootstrap.conf"
CONF_DIR="/etc/${PRODUCT}"
CERTS_DIR="${CONF_DIR}/certs"
CERT="${CERTS_DIR}/fullchain.pem"
KEY="${CERTS_DIR}/privkey.pem"
STATE_DIR="/var/lib/${PRODUCT}"
LOG_DIR="/var/log/${PRODUCT}"
DB="${STATE_DIR}/${PRODUCT}.db"
LOG="${LOG_DIR}/${PRODUCT}.log"

SERVICE_USER="${PRODUCT}"
SERVICE_GROUP="${PRODUCT}"

TARGET_VERSION="latest"
# auto — самоподписанный сертификат только пока в certs/ нет файла, похожего на
# сертификат; yes — всегда; no — никогда.
DEBUG_MODE="auto"

# Ответы конфигуратора. Пустые значения означают «спросить» (если есть терминал)
# или «не хватает ответа» (если терминала нет).
DOMAIN=""
ACME_EMAIL=""
ACME_MODE="http"
ACME_DIRECTORY=""
ACME_DNS_HOOK=""
PANEL_ACCESS=""
MASQUERADE=""
MASQUERADE_FILE=""
MASQUERADE_PROXY=""
PASSWORD_FILE=""
GENERATE_PASSWORD=0
ASSUME_YES=0

# Как будет задан пароль: stdin — уже спрошен и лежит в ADMIN_PASSWORD; file —
# прочитать из PASSWORD_FILE; generate — придумать при записи настроек.
PASSWORD_MODE=""
ADMIN_PASSWORD=""

# Состояние терминала для скрытого ввода. Пустая строка — эхо не выключали.
STTY_STATE=""
HIDDEN=""
TMP=""

usage() {
	cat <<EOF
Установщик ${PRODUCT}.

Использование:
  sh install.sh [флаги]

Флаги:
  --version ВЕРСИЯ       версия из GitHub Releases (по умолчанию latest)
  --debug                разрешить самоподписанный сертификат, пока настоящего нет
  --no-debug             не разрешать самоподписанный сертификат
  --yes                  не спрашивать подтверждение перед установкой
  --help                 показать эту справку

Ответы конфигуратора:
  --domain ИМЯ           домен сервера, например example.com
  --email АДРЕС          e-mail для ACME
  --acme-mode РЕЖИМ      http (порт 80) или dns (запись TXT через хук)
  --acme-directory URL   адрес ACME-каталога (по умолчанию Let's Encrypt)
  --acme-dns-hook ФАЙЛ   исполняемая программа для записей TXT при dns
  --access РЕЖИМ         tunnel (SSH-туннель) или public (снаружи)
  --masquerade РЕЖИМ     off, file или proxy
  --masquerade-file КАТ  каталог сайта для file
  --masquerade-proxy URL адрес зеркала для proxy
  --password-file ФАЙЛ   файл с паролем администратора (одна строка)
  --generate-password    сгенерировать пароль и показать его один раз

С терминалом скрипт спрашивает недостающие ответы и показывает их перед началом
установки. Без терминала нужны все ответы и один из флагов пароля: скрипт не
завершается успешно, пока они не собраны, и не ждёт ввода.

Повторный запуск на уже настроенной панели не трогает базу и ответы
конфигуратора: обновляются только бинарь, юнит и режим сертификата.

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

# trim убирает пробелы по краям ответа.
trim() {
	printf '%s' "$1" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

valid_domain() {
	case "$1" in
	*[[:space:]]*) return 1 ;;
	esac

	[ "${#1}" -le 253 ] || return 1
	printf '%s' "$1" | grep -Eq '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$'
}

valid_email() {
	case "$1" in
	*[[:space:]]*) return 1 ;;
	esac

	printf '%s' "$1" | grep -Eq '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'
}

valid_proxy() {
	case "$1" in
	*[[:space:]]*) return 1 ;;
	http://?*|https://?*) return 0 ;;
	*) return 1 ;;
	esac
}

# restore_terminal возвращает эхо, если read_hidden не успел это сделать сам.
restore_terminal() {
	if [ -n "$STTY_STATE" ]; then
		stty "$STTY_STATE" <&0 2>/dev/null || true
		STTY_STATE=""
	fi
}

cleanup() {
	restore_terminal
	if [ -n "$TMP" ]; then
		rm -rf "$TMP"
	fi
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# read_hidden читает строку, не показывая её на экране. Результат кладётся в
# HIDDEN, а не печатается: команда в подоболочке не увидела бы состояние
# терминала и не вернула бы эхо при прерывании.
read_hidden() {
	[ -t 0 ] || die "скрытый ввод пароля возможен только с терминала"

	printf '%s' "$1" >&2
	STTY_STATE="$(stty -g <&0)" || die "не удалось прочитать настройки терминала"
	stty -echo <&0 || {
		STTY_STATE=""
		die "не удалось отключить эхо"
	}

	HIDDEN=""
	if ! IFS= read -r HIDDEN; then
		restore_terminal
		printf '\n' >&2
		die "ввод пароля оборван"
	fi

	restore_terminal
	printf '\n' >&2
}

ask_domain() {
	while :; do
		printf 'Домен сервера (например example.com): ' >&2
		if ! IFS= read -r DOMAIN; then
			die "ввод домена оборван"
		fi

		DOMAIN="$(trim "$DOMAIN" | tr '[:upper:]' '[:lower:]')"
		if valid_domain "$DOMAIN"; then
			return
		fi

		warn "нужно имя вида example.com, без схемы, пути и порта"
	done
}

ask_email() {
	while :; do
		printf 'E-mail для ACME: ' >&2
		if ! IFS= read -r ACME_EMAIL; then
			die "ввод e-mail оборван"
		fi

		ACME_EMAIL="$(trim "$ACME_EMAIL")"
		if valid_email "$ACME_EMAIL"; then
			return
		fi

		warn "нужен полный адрес вида name@example.com"
	done
}

ask_access() {
	while :; do
		printf 'Режим доступа к панели (tunnel — SSH-туннель, public — снаружи) [tunnel]: ' >&2
		if ! IFS= read -r PANEL_ACCESS; then
			die "ввод режима доступа оборван"
		fi

		PANEL_ACCESS="$(trim "$PANEL_ACCESS")"
		[ -n "$PANEL_ACCESS" ] || PANEL_ACCESS="tunnel"

		case "$PANEL_ACCESS" in
		tunnel|public) return ;;
		*) warn "режим доступа: tunnel или public" ;;
		esac
	done
}

ask_masquerade() {
	while :; do
		printf 'Masquerade (off — выключен, file — свой сайт, proxy — зеркало) [off]: ' >&2
		if ! IFS= read -r MASQUERADE; then
			die "ввод режима masquerade оборван"
		fi

		MASQUERADE="$(trim "$MASQUERADE")"
		[ -n "$MASQUERADE" ] || MASQUERADE="off"

		case "$MASQUERADE" in
		off) return ;;
		file)
			ask_masquerade_file
			return
			;;
		proxy)
			ask_masquerade_proxy
			return
			;;
		*) warn "masquerade: off, file или proxy" ;;
		esac
	done
}

ask_masquerade_file() {
	while :; do
		printf 'Каталог сайта для masquerade: ' >&2
		if ! IFS= read -r MASQUERADE_FILE; then
			die "ввод каталога сайта оборван"
		fi

		MASQUERADE_FILE="$(trim "$MASQUERADE_FILE")"
		if [ -d "$MASQUERADE_FILE" ]; then
			return
		fi

		warn "каталога $MASQUERADE_FILE нет: masquerade file отдаёт файлы из существующего каталога"
	done
}

ask_masquerade_proxy() {
	while :; do
		printf 'Адрес зеркала (http:// или https://): ' >&2
		if ! IFS= read -r MASQUERADE_PROXY; then
			die "ввод адреса зеркала оборван"
		fi

		MASQUERADE_PROXY="$(trim "$MASQUERADE_PROXY")"
		if valid_proxy "$MASQUERADE_PROXY"; then
			return
		fi

		warn "адрес зеркала должен начинаться с http:// или https://"
	done
}

ask_password() {
	while :; do
		read_hidden 'Пароль администратора (пусто — сгенерировать): '
		if [ -z "$HIDDEN" ]; then
			PASSWORD_MODE="generate"
			ADMIN_PASSWORD=""
			return
		fi

		PASSWORD_FIRST="$HIDDEN"
		read_hidden 'Повторите пароль: '
		if [ "$PASSWORD_FIRST" != "$HIDDEN" ]; then
			warn "пароли не совпали"
			continue
		fi

		PASSWORD_MODE="stdin"
		ADMIN_PASSWORD="$PASSWORD_FIRST"
		return
	done
}

# show_answers показывает то, что будет записано, до первого изменения в системе.
show_answers() {
	case "$PANEL_ACCESS" in
	tunnel) ACCESS_TEXT="SSH-туннель, 127.0.0.1:8443" ;;
	public) ACCESS_TEXT="публичный, 0.0.0.0:8443" ;;
	esac

	case "$MASQUERADE" in
	file) MASQUERADE_TEXT="file, ${MASQUERADE_FILE}" ;;
	proxy) MASQUERADE_TEXT="proxy, ${MASQUERADE_PROXY}" ;;
	off) MASQUERADE_TEXT="off" ;;
	esac

	case "$ACME_MODE" in
	dns) ACME_TEXT="DNS-01, хук ${ACME_DNS_HOOK}" ;;
	*) ACME_TEXT="HTTP-01" ;;
	esac
	if [ -n "$ACME_DIRECTORY" ]; then
		ACME_TEXT="${ACME_TEXT}, каталог ${ACME_DIRECTORY}"
	fi

	case "$PASSWORD_MODE" in
	generate) PASSWORD_TEXT="будет сгенерирован и показан один раз" ;;
	file) PASSWORD_TEXT="из файла ${PASSWORD_FILE}" ;;
	stdin) PASSWORD_TEXT="задан" ;;
	esac

	cat >&2 <<EOF

Будет установлено:
  домен:        ${DOMAIN}
  e-mail ACME:  ${ACME_EMAIL}
  сертификат:   ${ACME_TEXT}
  панель:       ${ACCESS_TEXT}
  masquerade:   ${MASQUERADE_TEXT}
  пароль:       ${PASSWORD_TEXT}

EOF
}

# validate_answers проверяет всё, что можно проверить до изменений. Ошибка здесь
# не стоит ни установленного пакета, ни заведённого пользователя.
validate_answers() {
	DOMAIN="$(trim "$DOMAIN" | tr '[:upper:]' '[:lower:]')"
	ACME_EMAIL="$(trim "$ACME_EMAIL")"
	PANEL_ACCESS="$(trim "$PANEL_ACCESS")"
	MASQUERADE="$(trim "$MASQUERADE")"

	valid_domain "$DOMAIN" ||
		die "домен должен быть вида example.com, без схемы, пути и порта: $DOMAIN"
	valid_email "$ACME_EMAIL" ||
		die "e-mail должен быть полным адресом вида name@example.com: $ACME_EMAIL"

	ACME_MODE="$(trim "$ACME_MODE")"
	ACME_DIRECTORY="$(trim "$ACME_DIRECTORY")"
	ACME_DNS_HOOK="$(trim "$ACME_DNS_HOOK")"

	case "$ACME_MODE" in
	http)
		[ -z "$ACME_DNS_HOOK" ] ||
			die "для проверки ACME http не нужен --acme-dns-hook"
		;;
	dns)
		[ -n "$ACME_DNS_HOOK" ] ||
			die "для проверки ACME dns нужен --acme-dns-hook"
		if [ ! -f "$ACME_DNS_HOOK" ] || [ ! -x "$ACME_DNS_HOOK" ]; then
			die "хук DNS должен быть исполняемым файлом: $ACME_DNS_HOOK"
		fi
		;;
	*)
		die "способ проверки ACME: http или dns, получено: $ACME_MODE"
		;;
	esac

	if [ -n "$ACME_DIRECTORY" ]; then
		case "$ACME_DIRECTORY" in
		http://?*|https://?*) ;;
		*) die "адрес ACME-каталога должен начинаться с http:// или https://: $ACME_DIRECTORY" ;;
		esac
	fi

	case "$PANEL_ACCESS" in
	tunnel|public) ;;
	*) die "режим доступа: tunnel или public, получено: $PANEL_ACCESS" ;;
	esac

	case "$MASQUERADE" in
	off)
		[ -z "$MASQUERADE_FILE" ] && [ -z "$MASQUERADE_PROXY" ] ||
			die "для masquerade off не нужны --masquerade-file и --masquerade-proxy"
		;;
	file)
		[ -n "$MASQUERADE_FILE" ] ||
			die "для masquerade file нужен --masquerade-file"
		[ -z "$MASQUERADE_PROXY" ] ||
			die "для masquerade file не нужен --masquerade-proxy"
		[ -d "$MASQUERADE_FILE" ] ||
			die "каталога сайта $MASQUERADE_FILE нет"
		;;
	proxy)
		[ -n "$MASQUERADE_PROXY" ] ||
			die "для masquerade proxy нужен --masquerade-proxy"
		[ -z "$MASQUERADE_FILE" ] ||
			die "для masquerade proxy не нужен --masquerade-file"
		valid_proxy "$MASQUERADE_PROXY" ||
			die "адрес зеркала должен начинаться с http:// или https://: $MASQUERADE_PROXY"
		;;
	*) die "masquerade: off, file или proxy, получено: $MASQUERADE" ;;
	esac

	case "$PASSWORD_MODE" in
	stdin)
		[ -n "$ADMIN_PASSWORD" ] || die "пустой пароль администратора"
		;;
	file)
		[ -n "$PASSWORD_FILE" ] || die "не задан --password-file"
		[ -f "$PASSWORD_FILE" ] || die "файла с паролем нет: $PASSWORD_FILE"
		[ -r "$PASSWORD_FILE" ] || die "файл с паролем не читается: $PASSWORD_FILE"

		ADMIN_PASSWORD=""
		IFS= read -r ADMIN_PASSWORD <"$PASSWORD_FILE" || true
		[ -n "$ADMIN_PASSWORD" ] || die "файл с паролем пуст: $PASSWORD_FILE"
		;;
	generate) ;;
	*) die "не задан пароль администратора: нужен --password-file или --generate-password" ;;
	esac

	if [ "$PASSWORD_MODE" != "generate" ] && [ "$ADMIN_PASSWORD" = "admin" ]; then
		die "пароль администратора не должен совпадать с паролем по умолчанию admin"
	fi
}

# run_setup собирает общие ответы и вызывает команду настройки.
#
# Функция выполняется в подоболочке: set -- меняет её собственные позиционные
# параметры, а не аргументы установщика.
run_setup() (
	binary="$1"
	action="$2"
	runner="${3:-}"

	set -- setup --db "$DB" --domain "$DOMAIN" --email "$ACME_EMAIL" \
		--access "$PANEL_ACCESS" --masquerade "$MASQUERADE" \
		--acme-mode "$ACME_MODE" \
		--cert "$CERT" --key "$KEY"

	if [ -n "$ACME_DIRECTORY" ]; then
		set -- "$@" --acme-directory "$ACME_DIRECTORY"
	fi
	if [ -n "$ACME_DNS_HOOK" ]; then
		set -- "$@" --acme-dns-hook "$ACME_DNS_HOOK"
	fi

	case "$MASQUERADE" in
	file) set -- "$@" --masquerade-file "$MASQUERADE_FILE" ;;
	proxy) set -- "$@" --masquerade-proxy "$MASQUERADE_PROXY" ;;
	esac

	case "$action" in
	check) set -- "$@" --check ;;
	stdin) set -- "$@" --password-stdin ;;
	generate) set -- "$@" --generate-password ;;
	esac

	# Проверка читает только ответы и каталог сайта, поэтому идёт от root.
	# Запись в базу — от имени службы: файл должен принадлежать тому, кто будет
	# его открывать при каждом запуске панели.
	if [ "$runner" = "service" ]; then
		runuser -u "$SERVICE_USER" -- "$binary" "$@"
	else
		"$binary" "$@"
	fi
)

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
	--yes)
		ASSUME_YES=1
		shift
		;;
	--domain)
		[ "$#" -ge 2 ] || die "после --domain нужен домен"
		DOMAIN="$2"
		shift 2
		;;
	--domain=*)
		DOMAIN="${1#*=}"
		shift
		;;
	--email)
		[ "$#" -ge 2 ] || die "после --email нужен адрес"
		ACME_EMAIL="$2"
		shift 2
		;;
	--email=*)
		ACME_EMAIL="${1#*=}"
		shift
		;;
	--acme-mode)
		[ "$#" -ge 2 ] || die "после --acme-mode нужен режим"
		ACME_MODE="$2"
		shift 2
		;;
	--acme-mode=*)
		ACME_MODE="${1#*=}"
		shift
		;;
	--acme-directory)
		[ "$#" -ge 2 ] || die "после --acme-directory нужен адрес"
		ACME_DIRECTORY="$2"
		shift 2
		;;
	--acme-directory=*)
		ACME_DIRECTORY="${1#*=}"
		shift
		;;
	--acme-dns-hook)
		[ "$#" -ge 2 ] || die "после --acme-dns-hook нужен путь"
		ACME_DNS_HOOK="$2"
		shift 2
		;;
	--acme-dns-hook=*)
		ACME_DNS_HOOK="${1#*=}"
		shift
		;;
	--access)
		[ "$#" -ge 2 ] || die "после --access нужен режим"
		PANEL_ACCESS="$2"
		shift 2
		;;
	--access=*)
		PANEL_ACCESS="${1#*=}"
		shift
		;;
	--masquerade)
		[ "$#" -ge 2 ] || die "после --masquerade нужен режим"
		MASQUERADE="$2"
		shift 2
		;;
	--masquerade=*)
		MASQUERADE="${1#*=}"
		shift
		;;
	--masquerade-file)
		[ "$#" -ge 2 ] || die "после --masquerade-file нужен каталог"
		MASQUERADE_FILE="$2"
		shift 2
		;;
	--masquerade-file=*)
		MASQUERADE_FILE="${1#*=}"
		shift
		;;
	--masquerade-proxy)
		[ "$#" -ge 2 ] || die "после --masquerade-proxy нужен адрес"
		MASQUERADE_PROXY="$2"
		shift 2
		;;
	--masquerade-proxy=*)
		MASQUERADE_PROXY="${1#*=}"
		shift
		;;
	--password-file)
		[ "$#" -ge 2 ] || die "после --password-file нужен путь"
		PASSWORD_FILE="$2"
		shift 2
		;;
	--password-file=*)
		PASSWORD_FILE="${1#*=}"
		shift
		;;
	--generate-password)
		GENERATE_PASSWORD=1
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

if [ -n "$PASSWORD_FILE" ] && [ "$GENERATE_PASSWORD" -eq 1 ]; then
	die "пароль задаётся либо --password-file, либо --generate-password"
fi

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
command -v runuser >/dev/null 2>&1 || die "не найден runuser: нужен пакет util-linux"

info "система: $OS_NAME, linux/$ARCH"

# --- ответы конфигуратора ----------------------------------------------------

# Настроенная база — признак того, что панель уже прошла конфигуратор. Ответы
# повторного запуска к ней не применяются: домен, пароль и masquerade уже живут
# в базе, и переписывать их установщиком нельзя.
CONFIGURED=0
if [ -s "$DB" ]; then
	CONFIGURED=1
fi

if [ "$CONFIGURED" -eq 1 ]; then
	if [ -n "$DOMAIN$ACME_EMAIL$ACME_DIRECTORY$ACME_DNS_HOOK$PANEL_ACCESS$MASQUERADE$MASQUERADE_FILE$MASQUERADE_PROXY$PASSWORD_FILE" ] ||
		[ "$GENERATE_PASSWORD" -eq 1 ]; then
		warn "панель уже настроена: ответы конфигуратора не применяются"
	fi
else
	if [ -t 0 ]; then
		INTERACTIVE=1
	else
		INTERACTIVE=0
	fi

	if [ "$INTERACTIVE" -eq 0 ]; then
		MISSING=""
		[ -n "$DOMAIN" ] || MISSING="$MISSING --domain"
		[ -n "$ACME_EMAIL" ] || MISSING="$MISSING --email"
		[ -n "$PANEL_ACCESS" ] || MISSING="$MISSING --access"
		[ -n "$MASQUERADE" ] || MISSING="$MISSING --masquerade"

		[ -z "$MISSING" ] ||
			die "нет терминала: укажите ответы конфигуратора:$MISSING (см. --help)"

		if [ -z "$PASSWORD_FILE" ] && [ "$GENERATE_PASSWORD" -eq 0 ]; then
			die "нет терминала: задайте пароль через --password-file или --generate-password"
		fi
	fi

	if [ "$INTERACTIVE" -eq 1 ]; then
		[ -n "$DOMAIN" ] || ask_domain
		[ -n "$ACME_EMAIL" ] || ask_email
		[ -n "$PANEL_ACCESS" ] || ask_access
		[ -n "$MASQUERADE" ] || ask_masquerade

		if [ "$MASQUERADE" = "file" ] && [ -z "$MASQUERADE_FILE" ]; then
			ask_masquerade_file
		fi
		if [ "$MASQUERADE" = "proxy" ] && [ -z "$MASQUERADE_PROXY" ]; then
			ask_masquerade_proxy
		fi
	fi

	if [ "$GENERATE_PASSWORD" -eq 1 ]; then
		PASSWORD_MODE="generate"
	elif [ -n "$PASSWORD_FILE" ]; then
		PASSWORD_MODE="file"
	elif [ "$INTERACTIVE" -eq 1 ]; then
		ask_password
	fi

	validate_answers
	show_answers

	if [ "$INTERACTIVE" -eq 1 ] && [ "$ASSUME_YES" -eq 0 ]; then
		printf 'Продолжить установку? [y/N]: ' >&2
		if ! IFS= read -r REPLY; then
			die "подтверждение не получено"
		fi

		case "$(trim "$REPLY")" in
		y|Y|yes|YES|да|Да|ДА) ;;
		*)
			info "отменено"
			exit 0
			;;
		esac
	fi
fi

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
# Владелец должен быть один — тот, от которого работает панель.
if [ -e "$DB" ]; then
	chown "$SERVICE_USER:$SERVICE_GROUP" "$DB"
	chmod 0600 "$DB"
fi
if [ -e "$LOG" ]; then
	chown "$SERVICE_USER:$SERVICE_GROUP" "$LOG"
	chmod 0600 "$LOG"
fi

# Каталог сайта читает уже не root, а служба. Проверяем это от её имени: иначе
# установка дошла бы до записи базы и упала на первом же обращении к сайту.
if [ "$CONFIGURED" -eq 0 ] && [ "$MASQUERADE" = "file" ]; then
	if ! runuser -u "$SERVICE_USER" -- test -r "$MASQUERADE_FILE" ||
		! runuser -u "$SERVICE_USER" -- test -x "$MASQUERADE_FILE"; then
		die "каталог сайта $MASQUERADE_FILE недоступен пользователю $SERVICE_USER: нужны права на чтение и проход"
	fi
fi

# Хук DNS запускает служба, а не root: проверяем его от её имени.
if [ "$CONFIGURED" -eq 0 ] && [ "$ACME_MODE" = "dns" ]; then
	if ! runuser -u "$SERVICE_USER" -- test -x "$ACME_DNS_HOOK"; then
		die "хук DNS $ACME_DNS_HOOK недоступен пользователю $SERVICE_USER: нужен исполняемый файл"
	fi
fi

# --- загрузка релиза --------------------------------------------------------

TMP="$(mktemp -d "${TMPDIR:-/tmp}/hysteria-ward.XXXXXX")" ||
	die "не удалось создать временный каталог"

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

# Ответы проверяет тот же код, который будет их записывать. Проверка ничего не
# создаёт и не меняет: она ещё раз ловит опечатку до записи базы и запуска службы.
if [ "$CONFIGURED" -eq 0 ]; then
	info "проверяю ответы конфигуратора"
	run_setup "$BIN_SRC" check >/dev/null ||
		die "ответы конфигуратора не прошли проверку"
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

# --- начальная настройка ----------------------------------------------------

# Записываем ответы до запуска службы: панель должна подняться уже с доменом,
# доступом и паролем, а не оказаться на секунду открытой с паролем по умолчанию.
if [ "$CONFIGURED" -eq 0 ]; then
	info "сохраняю настройки"

	case "$PASSWORD_MODE" in
	stdin|file)
		printf '%s\n' "$ADMIN_PASSWORD" | run_setup "$BIN" stdin service ||
			die "не удалось сохранить настройки"
		;;
	generate)
		run_setup "$BIN" generate service ||
			die "не удалось сохранить настройки"
		;;
	*)
		die "не задан пароль администратора"
		;;
	esac
fi

# До появления настоящего сертификата панель не поднимется: ядро и интерфейс
# читают один и тот же tls. --debug разрешает интерфейсу (и только ему) выписать
# самоподписанный сертификат. Как только в certs/ появится файл, похожий на
# сертификат, режим выключается: самоподписанный — временная мера до ACME, а не
# рабочий режим.
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

if [ "$CONFIGURED" -eq 0 ]; then
	CERT_SUMMARY="Способ проверки: ${ACME_TEXT}."
else
	CERT_SUMMARY="Настройки ACME сохранены при установке."
fi

info "готово"
"$BIN" --version

cat <<EOF

Служба:   systemctl status ${PRODUCT}
База:     ${DB}
Журнал:   ${LOG}

Панель слушает 8443. В режиме SSH-туннеля она доступна только с петли:
  ssh -L 8443:127.0.0.1:8443 root@<адрес-сервера>
и зайти на https://127.0.0.1:8443

В публичном режиме панель слушает 8443 на всех интерфейсах — откройте её по
адресу https://panel.${DOMAIN}:8443 после выпуска сертификата.

Сертификат панель выпускает и продлевает сама. ${CERT_SUMMARY} Для HTTP-01
домен должен указывать на этот сервер, а порт 80 быть свободен; выпуск идёт в
фоне, панель при этом уже работает.

Пароль администратора задан при установке. Сменить его:
  systemctl stop ${PRODUCT}
  runuser -u ${PRODUCT} -- ${BIN} passwd
  systemctl start ${PRODUCT}
EOF

if [ "$PASSWORD_MODE" = "generate" ] && [ "$CONFIGURED" -eq 0 ]; then
	cat <<EOF

Пароль администратора напечатан выше один раз. Сохраните его: в базе лежит
только хеш, и восстановить пароль оттуда нельзя.
EOF
fi

if [ "$USE_DEBUG" -eq 1 ]; then
	cat <<EOF

Сертификата для домена пока нет, поэтому панель поднята с самоподписанным:
браузер предупредит о недоверенном сертификате. Это временный режим до выпуска
сертификата ACME. Панель выпустит его сама и продолжит работать; после появления
файла в ${CERTS_DIR} повторный запуск установщика уберёт временный режим.
Причину отказа выпуска и повтор смотрите в интерфейсе панели.
EOF
fi
