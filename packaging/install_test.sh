#!/bin/sh
# Проверки установщика, которые не требуют root и сети. Запускаются из make lint:
# синтаксис, справка на обоих языках, --lang до --help и отсутствие жёстко
# заданного адреса панели в итоговом блоке.
set -eu

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
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

grep -q 'https://panel\.' "$SCRIPT" && fail "в установщике остался жёсткий https://panel.<домен>"

# Английская таблица должна покрывать все ключи, которые установщик передаёт в
# translate. Русские строки — исходный каталог, поэтому проверяется только
# наличие перевода, а не отдельный список идентификаторов.
used=$(mktemp)
catalog=$(mktemp)
trap 'rm -f "$used" "$catalog"' EXIT

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

printf 'install.sh: проверки пройдены\n'
