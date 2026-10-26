#!/bin/sh
# Проверки установщика, которые не требуют root и сети. Запускаются из make lint:
# синтаксис, справка на обоих языках, --lang до --help, покрытие перевода,
# правила пары «домен сервера — домен панели» и мастер в псевдографике.
set -eu

HERE=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
SCRIPT="${HERE}/install.sh"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

sh -n "$SCRIPT" || fail "sh -n"

ru=$(sh "$SCRIPT" --help)
printf '%s' "$ru" | grep -q 'Установщик' || fail "русская справка по умолчанию"
printf '%s' "$ru" | grep -q 'Usage:' && fail "русская справка содержит английский заголовок"

en=$(sh "$SCRIPT" --help --lang en)
printf '%s' "$en" | grep -q 'Usage:' || fail "английская справка после --help --lang en"
printf '%s' "$en" | grep -q 'Установщик' && fail "английская справка содержит русский заголовок"

en_first=$(sh "$SCRIPT" --lang en --help)
printf '%s' "$en_first" | grep -q 'Usage:' || fail "английская справка при --lang en --help"

if sh "$SCRIPT" --lang fr --help >/dev/null 2>&1; then
	fail "неизвестный --lang принят"
fi

if sh "$SCRIPT" --lang --help >/dev/null 2>&1; then
	fail "пустой --lang принят"
fi

# --no-tui — обычный флаг: разбор не должен считать его лишним аргументом.
sh "$SCRIPT" --no-tui --help >/dev/null 2>&1 || fail "--no-tui не принят"

grep -q 'https://panel\.' "$SCRIPT" && fail "в установщике остался жёсткий https://panel.<домен>"

# Английская таблица должна покрывать все ключи, которые установщик передаёт в
# translate. Русские строки — исходный каталог, поэтому проверяется только
# наличие перевода, а не отдельный список идентификаторов.
used=$(mktemp)
catalog=$(mktemp)
harness=$(mktemp)
wizard=$(mktemp)
stub_dir=$(mktemp -d)
trap 'rm -f "$used" "$catalog" "$harness" "$wizard"; rm -rf "$stub_dir"' EXIT

grep -o "translate '[^']*'" "$SCRIPT" |
	sed "s/^translate '//; s/'$//" |
	sort -u >"$used"
awk '
	/^MESSAGES_EN=/{inside=1; next}
	inside && /^EOF$/{inside=0; next}
	inside && index($0,"|") {split($0,a,"|"); print a[1]}
' "$SCRIPT" | sort -u >"$catalog"

missing=$(comm -23 "$used" "$catalog")
[ -z "$missing" ] || fail "нет английского перевода: $missing"

duplicates=$(awk '
	/^MESSAGES_EN=/{inside=1; next}
	inside && /^EOF$/{inside=0; next}
	inside && index($0,"|") {split($0,a,"|"); print a[1]}
' "$SCRIPT" | sort | uniq -d)
[ -z "$duplicates" ] || fail "повторяющиеся ключи каталога: $duplicates"

# Проверки ответов без root: берём только функции до разбора аргументов и
# вызываем их напрямую. Так правила установщика не расходятся с store, даже если
# их правят отдельно.
marker=$(grep -n '^while \[ "\$#" -gt 0 \]; do' "$SCRIPT" | head -1 | cut -d: -f1)
[ -n "$marker" ] || fail "не найден разбор аргументов установщика"

head -n $((marker - 1)) "$SCRIPT" >"$harness"
cat >>"$harness" <<'HARNESS'

setup_base() {
	DOMAIN="$1"
	PANEL_ACCESS="$2"
	PANEL_MODE="${3:-}"
	PANEL_DOMAIN="${4:-}"
	ACME_EMAIL="admin@example.com"
	ACME_MODE="http"
	ACME_DIRECTORY=""
	ACME_DNS_PROVIDER=""
	ACME_DNS_HOOK=""
	ACME_DNS_TOKEN_FILE=""
	ACME_DNS_ZONE_ID=""
	MASQUERADE="off"
	MASQUERADE_FILE=""
	MASQUERADE_PROXY=""
	PASSWORD_MODE="generate"
	ADMIN_PASSWORD=""
}

expect_panel() {
	label="$1"
	setup_base "$2" "$3" "$4" "$5"
	validate_answers

	[ "$PANEL_MODE" = "$6" ] ||
		{ printf 'FAIL: %s: режим %s, ожидался %s\n' "$label" "$PANEL_MODE" "$6" >&2; exit 1; }
	[ "$PANEL_HOST" = "$7" ] ||
		{ printf 'FAIL: %s: домен %s, ожидался %s\n' "$label" "$PANEL_HOST" "$7" >&2; exit 1; }
}

expect_reject() {
	label="$1"
	setup_base "$2" "$3" "$4" "$5"

	if ( validate_answers ) >/dev/null 2>&1; then
		printf 'FAIL: %s: пара принята\n' "$label" >&2
		exit 1
	fi
}

# run_logged: успешная команда не печатает свой вывод, отказ — печатает причину и
# возвращает ненулевой код. Иначе установщик либо заливал бы экран, либо скрывал
# причину сбоя apt.
apt_log=$(mktemp)
if ! quiet=$(run_logged "$apt_log" "apt upgrade" sh -c 'echo много вывода; echo и ещё' 2>&1); then
	printf 'FAIL: run_logged отверг успешную команду\n' >&2
	exit 1
fi
[ -z "$quiet" ] || { printf 'FAIL: run_logged напечатал вывод успешной команды: %s\n' "$quiet" >&2; exit 1; }

if run_logged "$apt_log" "apt upgrade" sh -c 'echo причина отказа >&2; exit 9' >/dev/null 2>&1; then
	printf 'FAIL: run_logged принял отказ\n' >&2
	exit 1
fi
grep -q 'причина отказа' "$apt_log" || { printf 'FAIL: причина отказа не попала в журнал\n' >&2; exit 1; }
[ -z "$RUN_PID" ] || { printf 'FAIL: RUN_PID не сброшен после команды\n' >&2; exit 1; }
rm -f "$apt_log" "${apt_log}.status"

# Прерывание установщика не должно оставлять долгую команду работать в одиночку:
# cleanup убивает её по RUN_PID. Команда оставляет отметку о полученном сигнале —
# так проверка не зависит от того, как оболочка отдаёт код завершения и когда
# убирает зомби.
ready=$(mktemp)
killed=$(mktemp)
rm -f "$ready" "$killed"
sh -c "trap 'printf killed >\"$killed\"; exit 1' TERM; printf ready >\"$ready\"; while :; do sleep 0.1; done" &
long_pid=$!

# Ждём, пока команда поставит обработчик сигнала: иначе проверка стреляла бы в
# ещё не готовый процесс и падала бы случайно.
tries=0
while [ ! -s "$ready" ] && [ "$tries" -lt 50 ]; do
	tries=$((tries + 1))
	sleep 0.1
done
[ -s "$ready" ] || { printf 'FAIL: долгая команда не запустилась\n' >&2; exit 1; }

RUN_PID=$long_pid
cleanup

tries=0
while [ ! -s "$killed" ] && [ "$tries" -lt 30 ]; do
	tries=$((tries + 1))
	sleep 0.1
done
[ -s "$killed" ] || { printf 'FAIL: cleanup не остановил долгую команду\n' >&2; exit 1; }
rm -f "$ready" "$killed"
wait "$long_pid" 2>/dev/null || true

expect_panel "example.com tunnel" example.com tunnel "" "" subdomain panel.example.com
expect_panel "panel.* tunnel" panel.example.com tunnel "" "" server panel.example.com
expect_panel "явный поддомен" example.com tunnel "" admin.example.com subdomain admin.example.com
expect_panel "явный домен включает subdomain" example.com public "" admin.example.com subdomain admin.example.com
expect_panel "panel.* public уходит на поддомен" panel.example.com public "" "" subdomain panel.panel.example.com
expect_reject "public + server" example.com public server ""
expect_reject "чужой базовый домен" example.com tunnel "" panel.example.net
expect_reject "server с доменом" example.com tunnel server admin.example.com

printf 'install.sh: проверки домена панели пройдены\n'
HARNESS

sh "$harness" || exit 1

# --- мастер в псевдографике -------------------------------------------------
#
# Настоящей псевдографики в CI нет, а гонять pty ради этого — источник случайных
# падений. Поэтому whiptail подменяется стабом: он пишет полученные аргументы в
# журнал и отдаёт заранее заданные ответы. Проверяется именно то, что зависит от
# установщика: порядок окон, умолчания, повтор после негодного ответа и то, что
# ответы доходят до тех же переменных, что и текст.

cat >"$stub_dir/whiptail" <<'STUB'
#!/bin/sh
# Стаб whiptail: интерфейс уходит в stdout, ответ — в stderr, как у настоящего.
count=0
[ ! -f "$WHIPTAIL_COUNT" ] || count=$(cat "$WHIPTAIL_COUNT")
count=$((count + 1))
printf '%s' "$count" >"$WHIPTAIL_COUNT"
printf '%s\n' "$*" >>"$WHIPTAIL_LOG"

lines=$(awk 'END { print NR }' "$WHIPTAIL_ANSWERS")
[ "$count" -le "$lines" ] || exit 1
answer=$(sed -n "${count}p" "$WHIPTAIL_ANSWERS")

action=""
for arg in "$@"; do
	case "$arg" in
	--inputbox|--menu|--yesno|--msgbox) action="$arg" ;;
	esac
done

case "$action" in
--yesno)
	case "$answer" in
	yes) exit 0 ;;
	*) exit 1 ;;
	esac
	;;
--inputbox|--menu)
	[ "$answer" = "@fail" ] && exit 1
	printf '%s' "$answer" >&2
	exit 0
	;;
esac

exit 0
STUB
chmod +x "$stub_dir/whiptail"

wizard_run() {
	answers="$1"
	cat >"$wizard" <<'WIZARD'
TUI=1
DOMAIN=""
ACME_EMAIL=""
PANEL_ACCESS=""
PANEL_MODE=""
PANEL_DOMAIN=""
ACME_MODE=""
ACME_DNS_PROVIDER=""
MASQUERADE=""

ask_domain
ask_email
ask_access
ask_panel_mode
ask_panel_domain
ask_acme_mode
ask_masquerade

printf 'answers: %s|%s|%s|%s|%s|%s\n' \
	"$DOMAIN" "$ACME_EMAIL" "$PANEL_ACCESS" "$PANEL_MODE" "$PANEL_DOMAIN" "$MASQUERADE"
WIZARD

	head -n $((marker - 1)) "$SCRIPT" >"$wizard.tmp"
	cat "$wizard" >>"$wizard.tmp"

	: >"$stub_dir/log"
	rm -f "$stub_dir/count"
	printf '%s\n' "$answers" >"$stub_dir/answers"

	PATH="$stub_dir:$PATH" \
		WHIPTAIL_LOG="$stub_dir/log" \
		WHIPTAIL_ANSWERS="$stub_dir/answers" \
		WHIPTAIL_COUNT="$stub_dir/count" \
		sh "$wizard.tmp" 2>&1
}

# Ответы идут строками: по одной на окно. Пустая строка — Enter, то есть
# умолчание.
got=$(wizard_run "Example.COM
admin@example.com
tunnel
subdomain

http
off")
printf '%s\n' "$got" | grep -q 'answers: example.com|admin@example.com|tunnel|subdomain||off' ||
	fail "мастер не довёл ответы до переменных: $got"

log=$(cat "$stub_dir/log")
for expect in '--inputbox' '--menu' '--default-item tunnel' '--default-item subdomain' \
	'--default-item http' '--default-item off'; do
	printf '%s' "$log" | grep -q -- "$expect" || fail "мастер не открыл окно: $expect"
done
printf '%s' "$log" | grep -q 'Домен сервера' || fail "заголовок окна не переведён"

# Негодный ответ: окно повторяется, причина показывается в msgbox.
got=$(wizard_run "не домен

example.com
admin@example.com
tunnel
subdomain

http
off")
printf '%s\n' "$got" | grep -q 'answers: example.com|' || fail "мастер не повторил вопрос: $got"
grep -q -- '--msgbox' "$stub_dir/log" || fail "мастер не показал причину отказа"

# Отмена (Esc) — отказ от установки с кодом 0, а не ошибка.
cat >"$wizard" <<'WIZARD'
TUI=1
DOMAIN=""
ask_domain
WIZARD
head -n $((marker - 1)) "$SCRIPT" >"$wizard.tmp"
cat "$wizard" >>"$wizard.tmp"
: >"$stub_dir/log"
rm -f "$stub_dir/count"
printf '@fail\n' >"$stub_dir/answers"

status=0
cancel_out=$(PATH="$stub_dir:$PATH" \
	WHIPTAIL_LOG="$stub_dir/log" \
	WHIPTAIL_ANSWERS="$stub_dir/answers" \
	WHIPTAIL_COUNT="$stub_dir/count" \
	sh "$wizard.tmp" 2>&1) || status=$?
[ "$status" -eq 0 ] || fail "отмена мастера вернула код $status"
printf '%s' "$cancel_out" | grep -q 'отменено' || fail "отмена мастера не сказала «отменено»"

printf 'install.sh: проверки мастера пройдены\n'
printf 'install.sh: проверки пройдены\n'
