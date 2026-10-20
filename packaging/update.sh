#!/bin/sh
# Обновление Hysteria Ward до новой версии без потери данных.
#
# Скрипт скачивает релизный архив из GitHub Releases, сверяет контрольную сумму,
# подменяет бинарь и systemd-юнит, перезапускает службу и проверяет, что новая
# версия действительно поднялась. Если не поднялась — возвращает прежний бинарь и
# копию базы, снятую до обновления.
#
# Данные не пересоздаются: база, пользователи, пароль, домен и сертификаты лежат
# там же, где лежали. Миграции схемы выполняет сама панель при старте; перед этим
# скрипт снимает копию базы.
#
# Повторный запуск безопасен: если версия уже установлена, шаги пропускаются.
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
STATE_DIR="/var/lib/${PRODUCT}"
LOG_DIR="/var/log/${PRODUCT}"
DB="${STATE_DIR}/${PRODUCT}.db"
LOG="${LOG_DIR}/${PRODUCT}.log"
BACKUP_DIR="${STATE_DIR}/backups"

SERVICE_USER="${PRODUCT}"
SERVICE_GROUP="${PRODUCT}"

TARGET_VERSION="latest"
VERSION_GIVEN=0
DRY_RUN=0
STATUS=0
REPAIR=0
FORCE=0
ASSUME_YES=0

TMP=""
CHANGED=0
BACKUP=""
START_STAMP=""

usage() {
	cat <<EOF
Обновление ${PRODUCT}.

Использование:
  sh update.sh [флаги]

Флаги:
  --version ВЕРСИЯ  версия из GitHub Releases: v0.1.0 или latest (по умолчанию)
  --dry-run         показать план и ничего не менять
  --status          показать версию, службу, сертификат и порты (без прав root)
  --repair          привести настройки службы и базы в порядок, не меняя версию
  --force           переустановить ту же версию
  --yes             не спрашивать подтверждение
  --help            показать эту справку

Обновление скачивает архив, сверяет sha256, снимает копию базы, подменяет
бинарь и юнит и запускает службу. Если новая версия не поднялась, прежний
бинарь и копия базы возвращаются автоматически.

Данные не пересоздаются: пользователи, пароль, домен и сертификаты остаются на
месте. Повторный запуск пропускает уже применённые шаги.

Скрипт нужно запускать от root на уже установленной панели.
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

cleanup() {
	if [ -n "$TMP" ]; then
		rm -rf "$TMP"
	fi
}
trap cleanup EXIT

while [ "$#" -gt 0 ]; do
	case "$1" in
	--version)
		[ "$#" -ge 2 ] || die "после --version нужна версия"
		TARGET_VERSION="$2"
		VERSION_GIVEN=1
		shift 2
		;;
	--version=*)
		TARGET_VERSION="${1#*=}"
		VERSION_GIVEN=1
		shift
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--status)
		STATUS=1
		shift
		;;
	--repair)
		REPAIR=1
		shift
		;;
	--force)
		FORCE=1
		shift
		;;
	--yes)
		ASSUME_YES=1
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

if [ "$STATUS" -eq 1 ] && { [ "$DRY_RUN" -eq 1 ] || [ "$REPAIR" -eq 1 ] || [ "$VERSION_GIVEN" -eq 1 ] || [ "$FORCE" -eq 1 ]; }; then
	die "--status не сочетается с обновлением: он только показывает состояние"
fi
if [ "$REPAIR" -eq 1 ] && [ "$VERSION_GIVEN" -eq 1 ]; then
	die "--repair не меняет версию: обновление и починка — разные действия"
fi
if [ "$TARGET_VERSION" != "latest" ]; then
	printf '%s' "$TARGET_VERSION" | grep -Eq '^v[0-9][0-9A-Za-z._-]*$' ||
		die "версия должна выглядеть как v0.1.0 или latest, получено: $TARGET_VERSION"
fi

# --- состояние --------------------------------------------------------------

installed_version() {
	[ -x "$BIN" ] || return 1
	"$BIN" --version | awk '{print $2}'
}

service_state() {
	systemctl show -p ActiveState --value "$PRODUCT.service" 2>/dev/null || true
}

service_since() {
	systemctl show -p ActiveEnterTimestamp --value "$PRODUCT.service" 2>/dev/null || true
}

wait_stopped() {
	tries=0
	while [ "$tries" -lt 20 ]; do
		[ "$(service_state)" = "inactive" ] && return 0
		[ "$(service_state)" = "failed" ] && return 0
		tries=$((tries + 1))
		sleep 1
	done

	return 1
}

wait_active() {
	tries=0
	streak=0
	while [ "$tries" -lt 20 ]; do
		if [ "$(service_state)" = "active" ]; then
			streak=$((streak + 1))
			[ "$streak" -ge 3 ] && return 0
		else
			streak=0
		fi

		tries=$((tries + 1))
		sleep 1
	done

	return 1
}

start_service() {
	systemctl reset-failed "$PRODUCT.service" 2>/dev/null || true
	systemctl start "$PRODUCT.service" || true
}

compare_files() {
	[ -f "$1" ] && [ -f "$2" ] || return 1
	[ "$(sha256sum "$1" | awk '{print $1}')" = "$(sha256sum "$2" | awk '{print $1}')" ]
}

show_ports() {
	if ! command -v ss >/dev/null 2>&1; then
		echo "порты: ss не установлен"
		return
	fi

	lines="$(ss -lntup 2>/dev/null | grep "$PRODUCT" || true)"
	if [ -z "$lines" ]; then
		if [ "$(id -u)" -ne 0 ]; then
			# Без root ss не показывает имена процессов; остаются порты, которые
			# занимает панель и ядро.
			lines="$(ss -lntu 2>/dev/null | awk 'NR > 1 { n = split($5, parts, ":"); if (parts[n] == "443" || parts[n] == "8443" || parts[n] == "80") print }' || true)"
			if [ -n "$lines" ]; then
				echo "порты (без имён процессов, нужен root):"
				printf '%s\n' "$lines" | sed 's/^/  /'
				return
			fi
		fi

		echo "порты: служба ничего не слушает"
		return
	fi

	echo "порты:"
	printf '%s\n' "$lines" | sed 's/^/  /'
}

show_certificate() {
	cert_path="$CERT"
	# Настройки читаются, только когда база свободна: работающая панель держит
	# её на записи. Иначе остаётся путь по умолчанию.
	if [ "$(id -u)" -eq 0 ] && [ "$(service_state)" != "active" ]; then
		tls_line="$("$BIN" config --db "$DB" --check 2>/dev/null | grep '^TLS: ' || true)"
		if [ -n "$tls_line" ]; then
			cert_path="${tls_line#TLS: }"
			cert_path="${cert_path%%,*}"
		fi
	fi

	cert_dir="$(dirname "$cert_path")"
	if [ ! -x "$cert_dir" ]; then
		echo "сертификат: нет доступа к $cert_dir (запустите с root)"
		return
	fi
	if [ ! -f "$cert_path" ]; then
		echo "сертификат: ещё не выпущен ($cert_path)"
		return
	fi

	if command -v openssl >/dev/null 2>&1; then
		subject="$(openssl x509 -in "$cert_path" -noout -subject 2>/dev/null || true)"
		dates="$(openssl x509 -in "$cert_path" -noout -enddate 2>/dev/null || true)"
		names="$(openssl x509 -in "$cert_path" -noout -ext subjectAltName 2>/dev/null | tail -n +2 | tr -d ' ' || true)"

		echo "сертификат: $cert_path"
		[ -n "$subject" ] && echo "  $subject"
		[ -n "$names" ] && echo "  имена: $names"
		[ -n "$dates" ] && echo "  $dates"
	else
		echo "сертификат: $cert_path"
	fi
}

if [ "$STATUS" -eq 1 ]; then
	state="$(service_state)"
	echo "${PRODUCT}: $(installed_version 2>/dev/null || echo "не установлен")"
	if [ "$state" = "active" ]; then
		since="$(service_since)"
		if [ -n "$since" ]; then
			echo "служба: active, с $since"
		else
			echo "служба: active"
		fi
	else
		echo "служба: $state"
	fi
	if db_size="$(stat -c %s "$DB" 2>/dev/null)"; then
		echo "база: $DB (${db_size} байт)"
	else
		echo "база: $DB (нет доступа)"
	fi
	echo "журнал: $LOG"
	echo

	if [ ! -x "$STATE_DIR" ]; then
		echo "настройки: нет доступа к $STATE_DIR (запустите с root)"
	elif [ "$(service_state)" = "active" ] || [ "$(service_state)" = "activating" ]; then
		echo "настройки: недоступны, пока панель работает (база занята)"
	elif [ -s "$DB" ]; then
		settings_out="$("$BIN" config --db "$DB" --check 2>&1)" && settings_ok=1 || settings_ok=0
		if [ "$settings_ok" -eq 0 ]; then
			echo "настройки: не читаются"
		fi
		printf '%s\n' "$settings_out" | sed 's/^/  /'
	else
		echo "настройки: базы нет"
	fi

	echo
	show_certificate
	echo
	show_ports

	exit 0
fi

# --- предполётные проверки --------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "нужны права root: sudo sh $0"

if [ ! -x "$BIN" ]; then
	die "панель не установлена: нет $BIN (установка — install.sh)"
fi
if [ ! -s "$DB" ]; then
	die "нет базы $DB: панель ещё не настроена (установка — install.sh)"
fi
if [ ! -f "$UNIT" ]; then
	die "нет systemd-юнита $UNIT: установка не завершена (установка — install.sh)"
fi
[ -d /run/systemd/system ] || die "systemd не запущен: обновление рассчитано на systemd-сервер"

# curl нужен для скачивания релиза. На минимальном сервере его может не быть:
# ставим из штатного репозитория, а не отправляем администратора искать пакет.
if ! command -v curl >/dev/null 2>&1; then
	info "ставлю curl"
	export DEBIAN_FRONTEND=noninteractive
	apt-get -o DPkg::Lock::Timeout=120 update -qq ||
		die "не удалось обновить список пакетов"
	apt-get -o DPkg::Lock::Timeout=120 install -y --no-install-recommends curl ca-certificates ||
		die "не удалось поставить curl"
fi

for tool in curl tar gzip sha256sum systemctl runuser; do
	command -v "$tool" >/dev/null 2>&1 || die "не найден $tool: установите пакет, который его содержит"
done

if [ -r /etc/os-release ]; then
	# shellcheck disable=SC1091
	OS_ID="$(. /etc/os-release && printf '%s' "${ID:-}")"
	case "$OS_ID" in
	ubuntu|debian) ;;
	*) warn "дистрибутив $OS_ID не проверялся: поддерживаются Ubuntu LTS и Debian 12/13" ;;
	esac
fi

case "$(uname -m)" in
x86_64|amd64) ARCH="amd64" ;;
aarch64|arm64) ARCH="arm64" ;;
*) die "архитектура $(uname -m) не поддерживается: нужны x86_64 или aarch64" ;;
esac

CURRENT="$(installed_version || echo "неизвестна")"

# Локальный каталог вместо GitHub: им проверяют сам установщик и обновление.
if [ -n "${HYSTERIA_WARD_BASE_URL:-}" ]; then
	BASE="$HYSTERIA_WARD_BASE_URL"
	LOCAL_BASE=1
else
	LOCAL_BASE=0
fi

resolve_latest() {
	if [ "$LOCAL_BASE" -eq 1 ]; then
		# В локальном каталоге версии не объявлены: её покажет скачанный бинарь.
		TARGET_VERSION="latest"
		return
	fi

	url="$(curl -fsSIL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")" ||
		die "не удалось узнать последнюю версию: проверьте сеть"
	tag="${url##*/}"
	case "$tag" in
	v[0-9]*) TARGET_VERSION="$tag" ;;
	*) die "GitHub вернул неожиданный адрес последней версии: $url" ;;
	esac
}

if [ "$REPAIR" -eq 0 ] && [ "$TARGET_VERSION" = "latest" ]; then
	resolve_latest
fi

if [ "$LOCAL_BASE" -eq 0 ]; then
	if [ "$TARGET_VERSION" = "latest" ]; then
		DEFAULT_BASE="https://github.com/${REPO}/releases/latest/download"
	else
		DEFAULT_BASE="https://github.com/${REPO}/releases/download/${TARGET_VERSION}"
	fi
	BASE="${HYSTERIA_WARD_BASE_URL:-$DEFAULT_BASE}"
fi

ASSET="${PRODUCT}_linux_${ARCH}.tar.gz"

fetch() {
	url="$1"
	out="$2"

	curl --fail --silent --show-error --location \
		--retry 3 --retry-delay 2 --connect-timeout 15 \
		--output "$out" "$url" ||
		die "не удалось скачать $url"
}

# --- план и починка ---------------------------------------------------------

cert_found() {
	for candidate in "$CERTS_DIR"/*.crt "$CERTS_DIR"/*.pem; do
		[ -f "$candidate" ] || continue

		case "${candidate##*/}" in
		*cert*|*fullchain*|*crt*)
			return 0
			;;
		esac
	done

	return 1
}

print_plan() {
	if [ "$REPAIR" -eq 1 ]; then
		cat <<EOF
План починки:
  служба:       остановить ${PRODUCT}, если работает
  каталоги:     ${CONF_DIR}, ${CERTS_DIR}, ${STATE_DIR}, ${LOG_DIR}
  права:        база и журнал 0600, сертификаты 0600, каталог сертификатов 0700
  настройки:    дополнить недостающие поля в базе умолчаниями
  сертификат:   $(cert_found && echo "найден, временный режим выключить" || echo "не найден, включить временный режим")
  служба:       запустить и дождаться active
EOF
		return
	fi

	if [ "$TARGET_VERSION" = "latest" ]; then
		target_text="последняя (из локального каталога)"
	else
		target_text="$TARGET_VERSION"
	fi

	action="заменить"
	if [ "$CURRENT" = "$TARGET_VERSION" ] && [ "$FORCE" -eq 0 ]; then
		action="пропустить: уже установлена"
	fi

	cat <<EOF
План обновления:
  сейчас:       $CURRENT
  цель:         $target_text
  архив:        $ASSET
  база:         копия в ${BACKUP_DIR}/ перед заменой
  бинарь:       $action ($BIN)
  юнит:         заменить, если отличается ($UNIT)
  служба:       остановить, запустить и дождаться active
  откат:        прежний бинарь и копия базы, если служба не поднялась
EOF
}

if [ "$DRY_RUN" -eq 1 ]; then
	print_plan
	exit 0
fi

if [ "$REPAIR" -eq 0 ] && [ "$CURRENT" = "$TARGET_VERSION" ] && [ "$FORCE" -eq 0 ]; then
	info "версия $CURRENT уже установлена, обновлять нечего"

	if [ "$(service_state)" != "active" ]; then
		info "служба не работает, запускаю"
		systemctl reset-failed "$PRODUCT.service" 2>/dev/null || true
		systemctl start "$PRODUCT.service" || true
		wait_active || die "служба не поднялась: смотрите journalctl -u $PRODUCT"
	fi

	exit 0
fi

# --- загрузка релиза --------------------------------------------------------

TMP="$(mktemp -d "${TMPDIR:-/tmp}/hysteria-ward-update.XXXXXX")" ||
	die "не удалось создать временный каталог"

if [ "$REPAIR" -eq 0 ]; then
	info "скачиваю $ASSET ($TARGET_VERSION)"
	fetch "$BASE/$ASSET" "$TMP/$ASSET"
	fetch "$BASE/checksums.txt" "$TMP/checksums.txt"

	EXPECTED="$(awk -v name="$ASSET" '$2 == name { print $1; exit }' "$TMP/checksums.txt")"
	[ -n "$EXPECTED" ] || die "в checksums.txt нет строки для $ASSET"

	ACTUAL="$(sha256sum "$TMP/$ASSET" | awk '{print $1}')"
	[ "$ACTUAL" = "$EXPECTED" ] ||
		die "контрольная сумма $ASSET не сошлась: ожидалась $EXPECTED, получена $ACTUAL"

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
	TARGET_VERSION="$GOT_VERSION"
fi

if [ "$ASSUME_YES" -eq 0 ] && [ -t 0 ]; then
	printf 'Продолжить? [y/N]: ' >&2
	if ! IFS= read -r REPLY; then
		die "подтверждение не получено"
	fi

	case "$REPLY" in
	y|Y|yes|YES|да|Да|ДА) ;;
	*)
		info "отменено"
		exit 0
		;;
	esac
fi

# --- остановка и копия ------------------------------------------------------

START_STAMP="$(date '+%Y-%m-%d %H:%M:%S')"

if [ "$(service_state)" = "active" ]; then
	info "останавливаю службу"
	systemctl stop "$PRODUCT.service" || die "не удалось остановить службу"
fi
wait_stopped || warn "служба не перешла в inactive, продолжаю"

info "снимаю копию базы"
install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$BACKUP_DIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
CURRENT_TAG="$(printf '%s' "$CURRENT" | tr -c 'A-Za-z0-9._-' '_')"
if [ "$REPAIR" -eq 0 ]; then
	BACKUP="${BACKUP_DIR}/${PRODUCT}-${CURRENT_TAG}-${STAMP}.db"
else
	BACKUP="${BACKUP_DIR}/${PRODUCT}-repair-${STAMP}.db"
fi
cp -p "$DB" "$BACKUP" || die "не удалось снять копию базы"
chown "$SERVICE_USER:$SERVICE_GROUP" "$BACKUP"
chmod 0600 "$BACKUP"

# --- починка ----------------------------------------------------------------

repair_settings() {
	info "привожу каталоги и права в порядок"
	install -d -o root -g "$SERVICE_GROUP" -m 0750 "$CONF_DIR"
	install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$CERTS_DIR"
	install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0700 "$STATE_DIR"
	install -d -o "$SERVICE_USER" -g "$SERVICE_GROUP" -m 0750 "$LOG_DIR"

	chown "$SERVICE_USER:$SERVICE_GROUP" "$DB"
	chmod 0600 "$DB"
	if [ -e "$LOG" ]; then
		chown "$SERVICE_USER:$SERVICE_GROUP" "$LOG"
		chmod 0600 "$LOG"
	fi
	for file in "$CERTS_DIR"/*; do
		[ -f "$file" ] || continue
		chown "$SERVICE_USER:$SERVICE_GROUP" "$file"
		chmod 0600 "$file"
	done

	info "дополняю настройки из базы"
	if ! runuser -u "$SERVICE_USER" -- "$BIN" config --db "$DB" --repair; then
		warn "настройки не удалось привести в порядок"
		start_service
		wait_active || warn "служба не поднялась: смотрите journalctl -u $PRODUCT"
		exit 1
	fi

	if cert_found; then
		if [ -f "$DROPIN" ]; then
			info "настоящий сертификат найден, выключаю временный режим"
			rm -f "$DROPIN"
			rmdir "$DROPIN_DIR" 2>/dev/null || true
			CHANGED=1
		fi
	else
		install -d -m 0755 "$DROPIN_DIR"
		cat >"$TMP/bootstrap.conf" <<EOF
[Service]
ExecStart=
ExecStart=${BIN} serve --debug
EOF

		if [ -f "$DROPIN" ] && compare_files "$TMP/bootstrap.conf" "$DROPIN"; then
			info "временный режим уже включён, оставляю"
		else
			info "настоящего сертификата нет, включаю временный режим"
			install -m 0644 "$TMP/bootstrap.conf" "$DROPIN"
			CHANGED=1
		fi
	fi
}

# --- замена -----------------------------------------------------------------

rollback() {
	warn "новая версия не поднялась, возвращаю прежнюю"

	systemctl stop "$PRODUCT.service" 2>/dev/null || true
	wait_stopped || true

	if [ -f "${BIN}.rollback" ]; then
		mv -f "${BIN}.rollback" "$BIN" || warn "прежний бинарь не вернулся"
	fi

	if [ -n "$BACKUP" ] && [ -f "$BACKUP" ]; then
		cp -p "$BACKUP" "$DB" || warn "копия базы не вернулась"
		chown "$SERVICE_USER:$SERVICE_GROUP" "$DB" 2>/dev/null || true
		chmod 0600 "$DB" 2>/dev/null || true
	fi

	systemctl daemon-reload 2>/dev/null || true
	start_service

	if wait_active; then
		warn "прежняя версия $(installed_version 2>/dev/null || echo "неизвестна") снова работает"
	else
		warn "прежняя версия тоже не поднялась: смотрите journalctl -u $PRODUCT"
	fi

	warn "журнал новой версии:"
	journalctl -u "$PRODUCT.service" --since "$START_STAMP" --no-pager --lines=40 2>/dev/null | sed 's/^/  /' || true

	exit 1
}

if [ "$REPAIR" -eq 1 ]; then
	repair_settings

	if [ "$CHANGED" -eq 1 ]; then
		systemctl daemon-reload
	fi

	start_service
	if ! wait_active; then
		warn "служба не поднялась после починки"
		systemctl status --no-pager --lines=20 "$PRODUCT.service" 2>/dev/null || true
		journalctl -u "$PRODUCT.service" --no-pager --lines=40 2>/dev/null || true
		exit 1
	fi

	info "готово: настройки приведены в порядок, служба работает"
	"$BIN" --version

	exit 0
fi

info "устанавливаю $TARGET_VERSION"
install -m 0755 "$BIN_SRC" "${BIN}.new" || rollback
cp -p "$BIN" "${BIN}.rollback" || rollback
mv -f "${BIN}.new" "$BIN" || rollback

if ! compare_files "$UNIT_SRC" "$UNIT"; then
	info "обновляю systemd-юнит"
	install -m 0644 "$UNIT_SRC" "$UNIT" || rollback
	CHANGED=1
fi

if [ "$CHANGED" -eq 1 ]; then
	systemctl daemon-reload
fi

start_service
if ! wait_active; then
	rollback
fi

GOT_VERSION="$(installed_version || echo "")"
if [ "$GOT_VERSION" != "$TARGET_VERSION" ]; then
	warn "после запуска версия $GOT_VERSION, ожидалась $TARGET_VERSION"
	rollback
fi

rm -f "${BIN}.rollback"

info "готово: установлена $GOT_VERSION, служба работает"
"$BIN" --version
echo "Копия базы: $BACKUP"
