#!/bin/sh
# Установщик Hysteria Ward на чистый VPS.
#
# Скрипт рассчитан на Ubuntu LTS и Debian 12/13: он ставит зависимости штатным
# пакетным менеджером, заводит службу systemd, раскладывает каталоги и скачивает
# готовый бинарь из GitHub Releases. Компиляции на сервере нет.
#
# Установка спрашивает то, что нельзя угадать: язык, домен, почту для ACME,
# режим доступа к панели, режим и домен панели и masquerade. Пароль
# администратора не спрашивается: он генерируется при установке и печатается
# один раз в самом конце. Без терминала вопросы не задаются: все ответы должны
# прийти флагами, иначе скрипт сразу говорит, чего не хватает, вместо того чтобы
# ждать ввода, которого не будет.
#
# На первой установке скрипт обновляет систему (apt update и apt upgrade), а curl
# ставит сам, если его нет: на свежем минимальном образе его может не быть.
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
ACME_MODE=""
ACME_DIRECTORY=""
ACME_DNS_HOOK=""
ACME_DNS_PROVIDER=""
ACME_DNS_TOKEN_FILE=""
ACME_DNS_ZONE_ID=""
PANEL_ACCESS=""
MASQUERADE=""
MASQUERADE_FILE=""
MASQUERADE_PROXY=""
PASSWORD_FILE=""
GENERATE_PASSWORD=0
ASSUME_YES=0
SKIP_UPGRADE=0

# Как будет задан пароль: file — прочитать из PASSWORD_FILE; generate — придумать
# при записи настроек. По умолчанию пароль генерируется: угадываемого пароля по
# умолчанию в панели нет.
PASSWORD_MODE=""
ADMIN_PASSWORD=""

# Язык собственного вывода установщика. По умолчанию русский: локаль сервера на
# язык не влияет, английский выбирается явно через --lang или первым вопросом.
LANG_CODE="ru"
LANG_SET=0

# Режим домена панели и её отдельное имя. Пустой PANEL_MODE до проверки означает
# «не задан»: после проверки он становится subdomain. Пустой PANEL_DOMAIN — это
# panel.<домен>, то есть значение по умолчанию, которое не хранится в базе и
# переезжает вместе с доменом сервера.
PANEL_MODE=""
PANEL_DOMAIN=""
PANEL_HOST=""

TMP=""
# Временная копия токена Cloudflare: setup читает её от имени службы, а после
# записи в базу файл удаляется.
ACME_TOKEN_TMP=""

usage() {
	if [ "$LANG_CODE" = "en" ]; then
		cat <<EOF
${PRODUCT} installer.

Usage:
  sh install.sh [flags]

Flags:
  --lang ru|en           installer language (default: ru)
  --version VERSION      version from GitHub Releases (default: latest)
  --debug                allow a self-signed certificate until a real one exists
  --no-debug             do not allow a self-signed certificate
  --yes                  do not ask for confirmation before installing
  --skip-upgrade         do not upgrade the system before installing
  --help                 show this help

Installer answers:
  --domain NAME          server domain, for example example.com
  --email ADDRESS        e-mail for ACME
  --acme-mode MODE       http (port 80) or dns (TXT record at the DNS provider)
  --acme-directory URL   ACME directory URL (default: Let's Encrypt)
  --acme-dns-provider P  DNS provider: hook or cloudflare
  --acme-dns-hook FILE   executable program for TXT records in dns mode
  --acme-dns-token-file F file with a Cloudflare API token
                         (Zone.DNS Edit; Zone.Zone Read is also needed to find the zone)
  --acme-dns-zone-id ID  Cloudflare zone ID (optional)
  --access MODE          tunnel (SSH tunnel) or public (from outside)
  --panel-mode MODE      panel domain: server or subdomain (default: subdomain)
  --panel-domain NAME    separate panel domain; implies subdomain
  --masquerade MODE      off, file or proxy
  --masquerade-file DIR  site directory for file mode
  --masquerade-proxy URL mirror URL for proxy mode
  --password-file FILE   use your own administrator password
  --generate-password    kept for compatibility: the password is always generated

With a terminal the script asks for missing answers and shows them before
installing. Without a terminal all answers are required; the password is
generated and printed once at the end. On the first install it runs apt update
and apt upgrade -y; --skip-upgrade skips this step.

Running it again on a configured panel does not touch the database or the
installer answers: only the binary, unit and certificate mode are updated.

Run the script as root on Ubuntu LTS or Debian 12/13.
EOF
	else
		cat <<EOF
Установщик ${PRODUCT}.

Использование:
  sh install.sh [флаги]

Флаги:
  --lang ru|en           язык установщика (по умолчанию ru)
  --version ВЕРСИЯ       версия из GitHub Releases (по умолчанию latest)
  --debug                разрешить самоподписанный сертификат, пока настоящего нет
  --no-debug             не разрешать самоподписанный сертификат
  --yes                  не спрашивать подтверждение перед установкой
  --skip-upgrade         не обновлять систему перед установкой
  --help                 показать эту справку

Ответы конфигуратора:
  --domain ИМЯ           домен сервера, например example.com
  --email АДРЕС          e-mail для ACME
  --acme-mode РЕЖИМ      http (порт 80) или dns (запись TXT у DNS-провайдера)
  --acme-directory URL   адрес ACME-каталога (по умолчанию Let's Encrypt)
  --acme-dns-provider П  DNS-провайдер: hook или cloudflare
  --acme-dns-hook ФАЙЛ   исполняемая программа для записей TXT при dns
  --acme-dns-token-file Ф файл с API-токеном Cloudflare
                         (Zone.DNS Edit; для поиска зоны ещё Zone.Zone Read)
  --acme-dns-zone-id ID  идентификатор зоны Cloudflare (необязательно)
  --access РЕЖИМ         tunnel (SSH-туннель) или public (снаружи)
  --panel-mode РЕЖИМ     домен панели: server или subdomain (по умолчанию subdomain)
  --panel-domain ИМЯ     отдельный домен панели; подразумевает subdomain
  --masquerade РЕЖИМ     off, file или proxy
  --masquerade-file КАТ  каталог сайта для file
  --masquerade-proxy URL адрес зеркала для proxy
  --password-file ФАЙЛ   свой пароль администратора вместо сгенерированного
  --generate-password    оставлен для совместимости: пароль генерируется всегда

С терминалом скрипт спрашивает недостающие ответы и показывает их перед началом
установки. Без терминала нужны все ответы; пароль генерируется сам и печатается
один раз в конце. На первой установке выполняется apt update и apt upgrade -y;
--skip-upgrade пропускает этот шаг.

Повторный запуск на уже настроенной панели не трогает базу и ответы
конфигуратора: обновляются только бинарь, юнит и режим сертификата.

Скрипт нужно запускать от root на Ubuntu LTS или Debian 12/13.
EOF
	fi
}

# MESSAGES_EN — английский каталог. Ключ — русская строка, которую печатает
# установщик; значение — её перевод. Русский каталог не нужен: русские строки и
# есть исходный текст. Совпадение ищется по самому длинному префиксу, поэтому
# одна запись переводит и сообщение с переменной в конце. Динамические значения
# остаются аргументами printf, а не подставляются в текст через eval.
MESSAGES_EN=$(cat <<'EOF'
внимание|warning
ошибка|error
Пароль администратора (сохраните):|Administrator password (save it):
Домен сервера (например example.com):|Server domain (for example example.com):
E-mail для ACME:|E-mail for ACME:
Способ проверки ACME (http — порт 80, dns — TXT у DNS-провайдера) [http]:|ACME challenge (http — port 80, dns — TXT at the DNS provider) [http]:
DNS-провайдер (hook — своя программа, cloudflare — API) [hook]:|DNS provider (hook — your own program, cloudflare — API) [hook]:
Программа-хук для записей TXT:|Program hook for TXT records:
Файл с API-токеном Cloudflare (право Zone.DNS Edit):|File with a Cloudflare API token (Zone.DNS Edit permission):
Идентификатор зоны Cloudflare (необязательно, Enter — искать по домену):|Cloudflare zone ID (optional, Enter — find by domain):
Режим доступа к панели (tunnel — SSH-туннель, public — снаружи) [tunnel]:|Panel access (tunnel — SSH tunnel, public — from outside) [tunnel]:
Режим домена панели (server — домен сервера, subdomain — отдельное имя) [subdomain]:|Panel domain mode (server — server domain, subdomain — separate name) [subdomain]:
Домен панели (Enter — panel.<домен>):|Panel domain (Enter — panel.<domain>):
Masquerade (off — выключен, file — свой сайт, proxy — зеркало) [off]:|Masquerade (off — disabled, file — your site, proxy — mirror) [off]:
Каталог сайта для masquerade:|Site directory for masquerade:
Адрес зеркала (http:// или https://):|Mirror URL (http:// or https://):
Продолжить установку? [y/N]:|Continue installation? [y/N]:
ввод домена оборван|domain input interrupted
ввод e-mail оборван|e-mail input interrupted
ввод способа проверки ACME оборван|ACME challenge input interrupted
ввод DNS-провайдера оборван|DNS provider input interrupted
ввод хука DNS оборван|DNS hook input interrupted
ввод файла с токеном Cloudflare оборван|Cloudflare token file input interrupted
ввод идентификатора зоны Cloudflare оборван|Cloudflare zone ID input interrupted
ввод режима доступа оборван|access mode input interrupted
ввод режима домена панели оборван|panel domain mode input interrupted
ввод домена панели оборван|panel domain input interrupted
ввод режима masquerade оборван|masquerade mode input interrupted
ввод каталога сайта оборван|site directory input interrupted
ввод адреса зеркала оборван|mirror URL input interrupted
подтверждение не получено|confirmation not received
нужно имя вида example.com, без схемы, пути и порта|a name like example.com is required, without scheme, path or port
нужен полный адрес вида name@example.com|a full address like name@example.com is required
способ проверки ACME: http или dns|ACME challenge: http or dns
DNS-провайдер: hook или cloudflare|DNS provider: hook or cloudflare
нужен путь к исполняемой программе|an executable program path is required
токен читается из файла, чтобы не попасть в историю оболочки|the token is read from a file so it does not end up in shell history
режим доступа: tunnel или public|access mode: tunnel or public
режим домена панели: server или subdomain|panel domain mode: server or subdomain
домен панели должен быть в том же базовом домене|the panel domain must be in the same registrable domain
домен панели должен отличаться от домена сервера|the panel domain must differ from the server domain
публичная панель не может жить на домене сервера|a public panel cannot live on the server domain
masquerade: off, file или proxy|masquerade: off, file or proxy
адрес зеркала должен начинаться с http:// или https://|the mirror URL must start with http:// or https://
панель уже настроена: ответы конфигуратора не применяются|the panel is already configured: installer answers are not applied
отменено|cancelled
ввод языка оборван|language input interrupted
Будет установлено:|The following will be installed:
домен:|domain:
домен панели:|panel domain:
e-mail ACME:|ACME e-mail:
сертификат:|certificate:
панель:|panel:
masquerade:|masquerade:
пароль:|password:
SSH-туннель|SSH tunnel
публичный|public
хук|hook
каталог|directory
будет сгенерирован и показан в конце|will be generated and shown at the end
из файла|from file
задан|set
server (домен сервера)|server (server domain)
subdomain|subdomain
DNS: создайте A-записи:|DNS: create A records:
DNS-01: провайдер должен обслуживать обе зоны.|DNS-01: the provider must serve both zones.
HTTP-01: обе записи должны указывать на этот сервер, порт 80 свободен.|HTTP-01: both records must point to this server and port 80 must be free.
нужно имя вида panel.example.com, без схемы, пути и порта|a name like panel.example.com is required, without scheme, path or port
в режиме server домен панели не задаётся: он совпадает с доменом сервера|in server mode the panel domain is not set: it matches the server domain
публичная панель не может жить на домене сервера: выберите subdomain|a public panel cannot live on the server domain: choose subdomain
домен панели должен быть вида panel.example.com:|the panel domain must look like panel.example.com:
домен панели должен быть в том же базовом домене, что и|the panel domain must be in the same registrable domain as
режим домена панели: server или subdomain, получено:|panel domain mode: server or subdomain, got:
домен должен быть вида example.com, без схемы, пути и порта:|the domain must look like example.com, without scheme, path or port:
e-mail должен быть полным адресом вида name@example.com:|the e-mail must be a full address like name@example.com:
система:|system:
обновляю систему: apt update и apt upgrade|updating the system: apt update and apt upgrade
ставлю зависимости:|installing dependencies:
завожу группу|creating group
завожу пользователя|creating user
раскладываю каталоги|creating directories
скачиваю|downloading
распаковываю|unpacking
проверяю ответы конфигуратора|validating installer answers
бинарь уже тот же, оставляю|the binary is already the same, keeping it
ставлю бинарь|installing binary
systemd-юнит уже тот же, оставляю|the systemd unit is already the same, keeping it
ставлю systemd-юнит|installing systemd unit
сохраняю настройки|saving settings
временный режим уже включён, оставляю|temporary mode is already enabled, keeping it
настоящего сертификата ещё нет: панель поднимется с самоподписанным|there is no real certificate yet: the panel will start with a self-signed one
настоящий сертификат найден, выключаю временный режим|a real certificate was found, disabling temporary mode
перезапускаю службу|restarting the service
запускаю службу|starting the service
готово|done
служба не запустилась|the service did not start
Способ проверки:|Challenge:
Настройки ACME сохранены при установке.|ACME settings were saved during installation.
Панель уже настроена: адрес и сертификат не менялись.|The panel is already configured: its address and certificate were not changed.
Панель слушает 8443 на всех интерфейсах:|The panel listens on 8443 on all interfaces:
после выпуска сертификата.|after the certificate is issued.
Панель спрятана на петле. Проброс:|The panel is hidden on the loopback. Forward the port:
Открыть:|Open:
Имя сертификата:|Certificate name:
Для DNS-01 панель создаёт TXT-запись через Cloudflare. Токен уже в базе: файл с ним можно удалить.|For DNS-01 the panel creates a TXT record through Cloudflare. The token is already in the database: the file can be deleted.
Для DNS-01 панель вызывает указанный хук; он должен оставаться доступен службе.|For DNS-01 the panel calls the configured hook; it must remain available to the service.
Для HTTP-01 домен должен указывать на этот сервер, а порт 80 быть свободен.|For HTTP-01 the domain must point to this server and port 80 must be free.
нужны права root:|root is required:
не найден /etc/os-release: не удалось определить дистрибутив|/etc/os-release not found: cannot determine the distribution
Ubuntu не поддерживается: нужен LTS 20.04, 22.04, 24.04 или 26.04|Ubuntu is not supported: LTS 20.04, 22.04, 24.04 or 26.04 is required
Debian не поддерживается: нужны Debian 12 или 13|Debian is not supported: Debian 12 or 13 is required
дистрибутив не поддерживается: нужны Ubuntu LTS или Debian 12/13|the distribution is not supported: Ubuntu LTS or Debian 12/13 is required
архитектура не поддерживается: нужны x86_64 или aarch64|the architecture is not supported: x86_64 or aarch64 is required
systemd не запущен: установка рассчитана на systemd-сервер|systemd is not running: this installer targets a systemd server
не найден systemctl|systemctl not found
не найден runuser: нужен пакет util-linux|runuser not found: the util-linux package is required
не найден curl: установите его командой apt-get install -y curl|curl not found: install it with apt-get install -y curl
не найден sha256sum: установите пакет coreutils|sha256sum not found: install the coreutils package
нет терминала: укажите ответы конфигуратора:|no terminal: provide the installer answers:
(см. --help)|(see --help)
не удалось обновить список пакетов|could not update the package list
не удалось обновить систему|could not upgrade the system
не удалось поставить зависимости|could not install dependencies
не удалось подготовить файл с токеном Cloudflare|could not prepare the Cloudflare token file
каталог сайта|site directory
недоступен пользователю|is not accessible to user
нужны права на чтение и проход|read and execute permissions are required
файл с токеном Cloudflare|Cloudflare token file
хук DNS|DNS hook
нужен исполняемый файл|an executable file is required
не удалось создать временный каталог|could not create a temporary directory
не удалось скачать|could not download
в checksums.txt нет строки для|checksums.txt has no line for
контрольная сумма|checksum
не сошлась: ожидалась|does not match: expected
получена|got
не удалось распаковать|could not unpack
в архиве нет|the archive has no
скачанный бинарь не запускается|the downloaded binary does not start
в архиве бинарь для другой платформы:|the archive contains a binary for another platform:
в релизе оказалась отладочная сборка|the release contains a debug build
в архиве версия|the archive contains version
ожидалась|expected
ответы конфигуратора не прошли проверку|installer answers failed validation
не удалось сохранить настройки|could not save settings
не удалось получить сгенерированный пароль администратора|could not get the generated administrator password
не задан пароль администратора|administrator password is not set
не удалось включить службу|could not enable the service
не задан --password-file|--password-file is not set
файла с паролем нет:|password file not found:
файл с паролем не читается:|password file is not readable:
файл с паролем пуст:|password file is empty:
пустой пароль администратора|empty administrator password
пароль администратора не должен совпадать с паролем по умолчанию admin|the administrator password must not match the default password admin
для проверки ACME http не нужны настройки DNS|ACME http does not need DNS settings
для проверки ACME dns с хуком нужен --acme-dns-hook|ACME dns with a hook requires --acme-dns-hook
для DNS-01 с хуком не нужны настройки Cloudflare|DNS-01 with a hook does not need Cloudflare settings
хук DNS должен быть исполняемым файлом:|the DNS hook must be an executable file:
для DNS-01 Cloudflare нужен --acme-dns-token-file|DNS-01 Cloudflare requires --acme-dns-token-file
для DNS-01 Cloudflare не нужен --acme-dns-hook|DNS-01 Cloudflare does not need --acme-dns-hook
файла с токеном Cloudflare нет:|Cloudflare token file not found:
файл с токеном Cloudflare не читается:|Cloudflare token file is not readable:
DNS-провайдер: hook или cloudflare, получено:|DNS provider: hook or cloudflare, got:
способ проверки ACME: http или dns, получено:|ACME challenge: http or dns, got:
адрес ACME-каталога должен начинаться с http:// или https://:|the ACME directory URL must start with http:// or https://:
режим доступа: tunnel или public, получено:|access mode: tunnel or public, got:
для masquerade off не нужны --masquerade-file и --masquerade-proxy|masquerade off does not need --masquerade-file or --masquerade-proxy
для masquerade file нужен --masquerade-file|masquerade file requires --masquerade-file
для masquerade file не нужен --masquerade-proxy|masquerade file does not need --masquerade-proxy
каталога сайта|site directory
каталога сайта нет:|site directory not found:
каталог сайта недоступен пользователю|site directory is not accessible to user
файл с токеном Cloudflare недоступен пользователю|Cloudflare token file is not accessible to user
хук DNS недоступен пользователю|DNS hook is not accessible to user
каталога нет:|directory not found:
masquerade file отдаёт файлы из существующего каталога|masquerade file serves files from an existing directory
нет|not found
для masquerade proxy нужен --masquerade-proxy|masquerade proxy requires --masquerade-proxy
для masquerade proxy не нужен --masquerade-file|masquerade proxy does not need --masquerade-file
адрес зеркала должен начинаться с http:// или https://:|the mirror URL must start with http:// or https://:
masquerade: off, file или proxy, получено:|masquerade: off, file or proxy, got:
не задан пароль администратора: нужен --password-file или --generate-password|administrator password is not set: use --password-file or --generate-password
после --lang нужен ru или en|--lang requires ru or en
язык: ru или en, получено:|language: ru or en, got:
после --panel-mode нужен server или subdomain|--panel-mode requires server or subdomain
после --panel-domain нужен домен|--panel-domain requires a domain
неизвестный аргумент:|unknown argument:
версия должна выглядеть как v0.1.0 или latest, получено:|the version must look like v0.1.0 or latest, got:
после --version нужна версия|--version requires a version
после --domain нужен домен|--domain requires a domain
после --email нужен адрес|--email requires an address
после --acme-mode нужен режим|--acme-mode requires a mode
после --acme-directory нужен адрес|--acme-directory requires a URL
после --acme-dns-provider нужен провайдер|--acme-dns-provider requires a provider
после --acme-dns-hook нужен путь|--acme-dns-hook requires a path
после --acme-dns-token-file нужен путь|--acme-dns-token-file requires a path
после --acme-dns-zone-id нужен идентификатор|--acme-dns-zone-id requires an ID
после --access нужен режим|--access requires a mode
после --masquerade нужен режим|--masquerade requires a mode
после --masquerade-file нужен каталог|--masquerade-file requires a directory
после --masquerade-proxy нужен адрес|--masquerade-proxy requires a URL
после --password-file нужен путь|--password-file requires a path
пароль задаётся либо --password-file, либо --generate-password|the password is set by either --password-file or --generate-password
EOF
)

# translate переводит строку установщика на выбранный язык. Русский — исходный,
# поэтому для него возвращается та же строка.
translate() {
	if [ "$LANG_CODE" != "en" ]; then
		printf '%s' "$1"
		return
	fi

	printf '%s\n' "$MESSAGES_EN" | awk -F '|' -v text="$1" '
		{
			key = $1
			value = $0
			sub(/^[^|]*\|/, "", value)
			if (index(text, key) == 1 && length(key) > best) {
				best = length(key)
				best_value = value
			}
		}
		END {
			if (best > 0) {
				printf "%s%s", best_value, substr(text, best + 1)
			} else {
				printf "%s", text
			}
		}
	'
}

info() {
	printf '==> %s\n' "$(translate "$*")"
}

warn() {
	printf '%s: %s\n' "$(translate 'внимание')" "$(translate "$*")" >&2
}

# print_generated_password напоминает пароль, если он уже записан в базу. Вывод
# идёт в stderr: это подсказка при сбое, а не часть успешного результата.
print_generated_password() {
	if [ "${PASSWORD_MODE:-}" = "generate" ] && [ -n "${ADMIN_PASSWORD:-}" ]; then
		printf '\n%s %s\n' "$(translate 'Пароль администратора (сохраните):')" "$ADMIN_PASSWORD" >&2
	fi
}

die() {
	printf '%s: %s\n' "$(translate 'ошибка')" "$(translate "$*")" >&2

	# Если сбой случился после записи базы, пароль уже действует. Показываем его
	# и здесь: иначе установка сломалась бы ровно в тот момент, когда доступ
	# нужен для починки, а пароль остался бы только в хеше.
	print_generated_password
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

cleanup() {
	if [ -n "$TMP" ]; then
		rm -rf "$TMP"
	fi
	if [ -n "$ACME_TOKEN_TMP" ]; then
		rm -f "$ACME_TOKEN_TMP"
	fi
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

ask_domain() {
	while :; do
		printf '%s ' "$(translate 'Домен сервера (например example.com):')" >&2
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
		printf '%s ' "$(translate 'E-mail для ACME:')" >&2
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

ask_acme_mode() {
	while :; do
		printf '%s ' "$(translate 'Способ проверки ACME (http — порт 80, dns — TXT у DNS-провайдера) [http]:')" >&2
		if ! IFS= read -r ACME_MODE; then
			die "ввод способа проверки ACME оборван"
		fi

		ACME_MODE="$(trim "$ACME_MODE")"
		[ -n "$ACME_MODE" ] || ACME_MODE="http"

		case "$ACME_MODE" in
		http|dns) return ;;
		*) warn "способ проверки ACME: http или dns" ;;
		esac
	done
}

ask_acme_dns_provider() {
	while :; do
		printf '%s ' "$(translate 'DNS-провайдер (hook — своя программа, cloudflare — API) [hook]:')" >&2
		if ! IFS= read -r ACME_DNS_PROVIDER; then
			die "ввод DNS-провайдера оборван"
		fi

		ACME_DNS_PROVIDER="$(trim "$ACME_DNS_PROVIDER")"
		[ -n "$ACME_DNS_PROVIDER" ] || ACME_DNS_PROVIDER="hook"

		case "$ACME_DNS_PROVIDER" in
		hook|cloudflare) return ;;
		*) warn "DNS-провайдер: hook или cloudflare" ;;
		esac
	done
}

ask_acme_dns_hook() {
	while :; do
		printf '%s ' "$(translate 'Программа-хук для записей TXT:')" >&2
		if ! IFS= read -r ACME_DNS_HOOK; then
			die "ввод хука DNS оборван"
		fi

		ACME_DNS_HOOK="$(trim "$ACME_DNS_HOOK")"
		if [ -n "$ACME_DNS_HOOK" ]; then
			return
		fi

		warn "нужен путь к исполняемой программе"
	done
}

ask_acme_dns_token_file() {
	while :; do
		printf '%s ' "$(translate 'Файл с API-токеном Cloudflare (право Zone.DNS Edit):')" >&2
		if ! IFS= read -r ACME_DNS_TOKEN_FILE; then
			die "ввод файла с токеном Cloudflare оборван"
		fi

		ACME_DNS_TOKEN_FILE="$(trim "$ACME_DNS_TOKEN_FILE")"
		if [ -n "$ACME_DNS_TOKEN_FILE" ]; then
			return
		fi

		warn "токен читается из файла, чтобы не попасть в историю оболочки"
	done
}

ask_acme_dns_zone_id() {
	printf '%s ' "$(translate 'Идентификатор зоны Cloudflare (необязательно, Enter — искать по домену):')" >&2
	if ! IFS= read -r ACME_DNS_ZONE_ID; then
		die "ввод идентификатора зоны Cloudflare оборван"
	fi

	ACME_DNS_ZONE_ID="$(trim "$ACME_DNS_ZONE_ID")"
}

ask_access() {
	while :; do
		printf '%s ' "$(translate 'Режим доступа к панели (tunnel — SSH-туннель, public — снаружи) [tunnel]:')" >&2
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

# ask_language спрашивает язык первым вопросом. Вопрос двуязычный: на этом шаге
# ещё нет выбранного языка, а `--lang` его уже задал и вопроса не будет.
ask_language() {
	[ -t 0 ] || return 0
	[ "$LANG_SET" -eq 0 ] || return 0

	while :; do
		printf 'Язык установки / Installation language (ru/en) [ru]: ' >&2
		if ! IFS= read -r REPLY; then
			die "ввод языка оборван"
		fi

		REPLY="$(trim "$REPLY" | tr '[:upper:]' '[:lower:]')"
		case "$REPLY" in
		"") LANG_CODE="ru"; return ;;
		ru|en) LANG_CODE="$REPLY"; return ;;
		*) printf 'ru / en\n' >&2 ;;
		esac
	done
}

# ask_panel_mode спрашивает, где живёт имя панели. При публичном доступе выбор
# один: панель не может делить домен с ядром.
ask_panel_mode() {
	if [ "$PANEL_ACCESS" = "public" ]; then
		PANEL_MODE="subdomain"
		return
	fi

	while :; do
		printf '%s ' "$(translate 'Режим домена панели (server — домен сервера, subdomain — отдельное имя) [subdomain]:')" >&2
		if ! IFS= read -r PANEL_MODE; then
			die "ввод режима домена панели оборван"
		fi

		PANEL_MODE="$(trim "$PANEL_MODE" | tr '[:upper:]' '[:lower:]')"
		[ -n "$PANEL_MODE" ] || PANEL_MODE="subdomain"

		case "$PANEL_MODE" in
		server|subdomain) return ;;
		*) warn "режим домена панели: server или subdomain" ;;
		esac
	done
}

# ask_panel_domain спрашивает отдельное имя панели. Пустой ответ — panel.<домен>:
# это значение не хранится, чтобы панель переезжала вместе с доменом сервера.
ask_panel_domain() {
	[ "$PANEL_MODE" = "server" ] && return

	while :; do
		printf '%s ' "$(translate 'Домен панели (Enter — panel.<домен>):')" >&2
		if ! IFS= read -r PANEL_DOMAIN; then
			die "ввод домена панели оборван"
		fi

		PANEL_DOMAIN="$(trim "$PANEL_DOMAIN" | tr '[:upper:]' '[:lower:]')"
		if [ -z "$PANEL_DOMAIN" ] || [ "$PANEL_DOMAIN" = "panel.${DOMAIN}" ]; then
			PANEL_DOMAIN=""
			return
		fi
		if valid_domain "$PANEL_DOMAIN"; then
			return
		fi

		warn "нужно имя вида panel.example.com, без схемы, пути и порта"
	done
}

# base_domain — приблизительный базовый домен для проверки до скачивания бинаря.
# Точная проверка по публичному суффиксу живёт в Go; здесь достаточно отсечь
# очевидно чужой домен, чтобы не идти на установку с заведомо негодной парой.
base_domain() {
	printf '%s' "$1" | awk -F. '{ if (NF >= 2) print $(NF-1) "." $NF; else print $0 }'
}

ask_masquerade() {
	while :; do
		printf '%s ' "$(translate 'Masquerade (off — выключен, file — свой сайт, proxy — зеркало) [off]:')" >&2
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
		printf '%s ' "$(translate 'Каталог сайта для masquerade:')" >&2
		if ! IFS= read -r MASQUERADE_FILE; then
			die "ввод каталога сайта оборван"
		fi

		MASQUERADE_FILE="$(trim "$MASQUERADE_FILE")"
		if [ -d "$MASQUERADE_FILE" ]; then
			return
		fi

		warn "$(translate 'каталога нет:') $MASQUERADE_FILE — $(translate 'masquerade file отдаёт файлы из существующего каталога')"
	done
}

ask_masquerade_proxy() {
	while :; do
		printf '%s ' "$(translate 'Адрес зеркала (http:// или https://):')" >&2
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

# show_answers показывает то, что будет записано, до первого изменения в системе.
show_answers() {
	case "$PANEL_ACCESS" in
	tunnel) ACCESS_TEXT="$(translate 'SSH-туннель'), 127.0.0.1:8443" ;;
	public) ACCESS_TEXT="$(translate 'публичный'), 0.0.0.0:8443" ;;
	esac

	case "$MASQUERADE" in
	file) MASQUERADE_TEXT="file, ${MASQUERADE_FILE}" ;;
	proxy) MASQUERADE_TEXT="proxy, ${MASQUERADE_PROXY}" ;;
	off) MASQUERADE_TEXT="off" ;;
	esac

	case "$ACME_MODE" in
	dns)
		case "$ACME_DNS_PROVIDER" in
		cloudflare) ACME_TEXT="DNS-01, Cloudflare" ;;
		*) ACME_TEXT="DNS-01, $(translate 'хук') ${ACME_DNS_HOOK}" ;;
		esac
		;;
	*) ACME_TEXT="HTTP-01" ;;
	esac
	if [ -n "$ACME_DIRECTORY" ]; then
		ACME_TEXT="${ACME_TEXT}, $(translate 'каталог') ${ACME_DIRECTORY}"
	fi

	case "$PASSWORD_MODE" in
	generate) PASSWORD_TEXT="$(translate 'будет сгенерирован и показан в конце')" ;;
	file) PASSWORD_TEXT="$(translate 'из файла') ${PASSWORD_FILE}" ;;
	*) PASSWORD_TEXT="$(translate 'задан')" ;;
	esac

	if [ "$PANEL_MODE" = "server" ]; then
		PANEL_MODE_TEXT="$(translate 'server (домен сервера)')"
	else
		PANEL_MODE_TEXT="$(translate 'subdomain')"
	fi

	printf '\n%s\n' "$(translate 'Будет установлено:')" >&2
	printf '  %-16s %s\n' "$(translate 'домен:')" "$DOMAIN" >&2
	printf '  %-16s %s\n' "$(translate 'домен панели:')" "$PANEL_HOST" >&2
	printf '  %-16s %s\n' "$(translate 'e-mail ACME:')" "$ACME_EMAIL" >&2
	printf '  %-16s %s\n' "$(translate 'сертификат:')" "$ACME_TEXT" >&2
	printf '  %-16s %s (%s)\n' "$(translate 'панель:')" "$ACCESS_TEXT" "$PANEL_MODE_TEXT" >&2
	printf '  %-16s %s\n' "$(translate 'masquerade:')" "$MASQUERADE_TEXT" >&2
	printf '  %-16s %s\n' "$(translate 'пароль:')" "$PASSWORD_TEXT" >&2
	printf '\n' >&2
}

# show_dns печатает A-записи, которые нужны до выпуска сертификата. Проверять
# DNS блокирующе нельзя: запись могли создать только что, и установка не должна
# ждать распространения. Поэтому здесь только напоминание с адресом сервера.
show_dns() {
	SERVER_IP=""
	if command -v ip >/dev/null 2>&1; then
		SERVER_IP="$(ip -4 route get 1.1.1.1 2>/dev/null |
			awk '{ for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }')"
	fi
	[ -n "$SERVER_IP" ] || SERVER_IP="<IPv4>"

	printf '%s\n' "$(translate 'DNS: создайте A-записи:')" >&2
	printf '  %-32s A  %s\n' "$DOMAIN" "$SERVER_IP" >&2
	if [ "$PANEL_HOST" != "$DOMAIN" ]; then
		printf '  %-32s A  %s\n' "$PANEL_HOST" "$SERVER_IP" >&2
	fi

	case "$ACME_MODE" in
	dns)
		printf '%s\n' "$(translate 'DNS-01: провайдер должен обслуживать обе зоны.')" >&2
		;;
	*)
		printf '%s\n' "$(translate 'HTTP-01: обе записи должны указывать на этот сервер, порт 80 свободен.')" >&2
		;;
	esac
	printf '\n' >&2
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
	ACME_DNS_PROVIDER="$(trim "$ACME_DNS_PROVIDER")"
	ACME_DNS_HOOK="$(trim "$ACME_DNS_HOOK")"
	ACME_DNS_TOKEN_FILE="$(trim "$ACME_DNS_TOKEN_FILE")"
	ACME_DNS_ZONE_ID="$(trim "$ACME_DNS_ZONE_ID")"

	[ -n "$ACME_MODE" ] || ACME_MODE="http"

	case "$ACME_MODE" in
	http)
		[ -z "$ACME_DNS_PROVIDER$ACME_DNS_HOOK$ACME_DNS_TOKEN_FILE$ACME_DNS_ZONE_ID" ] ||
			die "для проверки ACME http не нужны настройки DNS"
		;;
	dns)
		[ -n "$ACME_DNS_PROVIDER" ] || ACME_DNS_PROVIDER="hook"

		case "$ACME_DNS_PROVIDER" in
		hook)
			[ -n "$ACME_DNS_HOOK" ] ||
				die "для проверки ACME dns с хуком нужен --acme-dns-hook"
			[ -z "$ACME_DNS_TOKEN_FILE$ACME_DNS_ZONE_ID" ] ||
				die "для DNS-01 с хуком не нужны настройки Cloudflare"
			if [ ! -f "$ACME_DNS_HOOK" ] || [ ! -x "$ACME_DNS_HOOK" ]; then
				die "хук DNS должен быть исполняемым файлом: $ACME_DNS_HOOK"
			fi
			;;
		cloudflare)
			[ -n "$ACME_DNS_TOKEN_FILE" ] ||
				die "для DNS-01 Cloudflare нужен --acme-dns-token-file"
			[ -z "$ACME_DNS_HOOK" ] ||
				die "для DNS-01 Cloudflare не нужен --acme-dns-hook"
			[ -f "$ACME_DNS_TOKEN_FILE" ] ||
				die "файла с токеном Cloudflare нет: $ACME_DNS_TOKEN_FILE"
			[ -r "$ACME_DNS_TOKEN_FILE" ] ||
				die "файл с токеном Cloudflare не читается: $ACME_DNS_TOKEN_FILE"
			;;
		*)
			die "DNS-провайдер: hook или cloudflare, получено: $ACME_DNS_PROVIDER"
			;;
		esac
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

	PANEL_MODE="$(trim "$PANEL_MODE" | tr '[:upper:]' '[:lower:]')"
	PANEL_DOMAIN="$(trim "$PANEL_DOMAIN" | tr '[:upper:]' '[:lower:]')"
	[ -n "$PANEL_MODE" ] || PANEL_MODE="subdomain"

	case "$PANEL_MODE" in
	server|subdomain) ;;
	*) die "режим домена панели: server или subdomain, получено: $PANEL_MODE" ;;
	esac

	if [ "$PANEL_MODE" = "server" ]; then
		[ -z "$PANEL_DOMAIN" ] ||
			die "в режиме server домен панели не задаётся: он совпадает с доменом сервера"
		[ "$PANEL_ACCESS" != "public" ] ||
			die "публичная панель не может жить на домене сервера: выберите subdomain"
		PANEL_HOST="$DOMAIN"
	else
		if [ -z "$PANEL_DOMAIN" ] || [ "$PANEL_DOMAIN" = "panel.${DOMAIN}" ]; then
			# Пустой Domain — это «panel.<домен>»: значение по умолчанию не
			# хранится, поэтому смена домена сервера переносит и панель.
			PANEL_DOMAIN=""
			PANEL_HOST="panel.${DOMAIN}"
		else
			valid_domain "$PANEL_DOMAIN" ||
				die "домен панели должен быть вида panel.example.com: $PANEL_DOMAIN"
			PANEL_HOST="$PANEL_DOMAIN"
		fi

		[ "$PANEL_HOST" != "$DOMAIN" ] ||
			die "домен панели должен отличаться от домена сервера: $PANEL_HOST"
		[ "$(base_domain "$DOMAIN")" = "$(base_domain "$PANEL_HOST")" ] ||
			die "домен панели должен быть в том же базовом домене, что и $DOMAIN: $PANEL_HOST"
	fi

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
			die "$(translate 'каталога сайта нет:') $MASQUERADE_FILE"
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
		--access "$PANEL_ACCESS" --panel-mode "$PANEL_MODE" \
		--masquerade "$MASQUERADE" \
		--acme-mode "$ACME_MODE" \
		--cert "$CERT" --key "$KEY"

	if [ -n "$PANEL_DOMAIN" ]; then
		set -- "$@" --panel-domain "$PANEL_DOMAIN"
	fi
	if [ -n "$ACME_DIRECTORY" ]; then
		set -- "$@" --acme-directory "$ACME_DIRECTORY"
	fi
	if [ -n "$ACME_DNS_PROVIDER" ]; then
		set -- "$@" --acme-dns-provider "$ACME_DNS_PROVIDER"
	fi
	if [ -n "$ACME_DNS_HOOK" ]; then
		set -- "$@" --acme-dns-hook "$ACME_DNS_HOOK"
	fi
	if [ -n "$ACME_DNS_TOKEN_FILE" ]; then
		set -- "$@" --acme-dns-token-file "$ACME_DNS_TOKEN_FILE"
	fi
	if [ -n "$ACME_DNS_ZONE_ID" ]; then
		set -- "$@" --acme-dns-zone-id "$ACME_DNS_ZONE_ID"
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

# --lang разбирается до --help: иначе `install.sh --help --lang en` напечатал бы
# русскую справку. Отдельный проход не меняет позиционные параметры — их
# разбирает основной цикл ниже.
LANG_ARG=""
LANG_ARG_SET=0
LANG_PENDING=0
for arg in "$@"; do
	if [ "$LANG_PENDING" -eq 1 ]; then
		LANG_ARG="$arg"
		LANG_ARG_SET=1
		LANG_PENDING=0
		continue
	fi

	case "$arg" in
	--lang)
		LANG_PENDING=1
		;;
	--lang=*)
		LANG_ARG="${arg#*=}"
		LANG_ARG_SET=1
		;;
	esac
done
if [ "$LANG_PENDING" -eq 1 ]; then
	die "после --lang нужен ru или en"
fi
if [ "$LANG_ARG_SET" -eq 1 ]; then
	case "$LANG_ARG" in
	ru|en)
		LANG_CODE="$LANG_ARG"
		LANG_SET=1
		;;
	*)
		die "язык: ru или en, получено: $LANG_ARG"
		;;
	esac
fi

while [ "$#" -gt 0 ]; do
	case "$1" in
	--lang)
		[ "$#" -ge 2 ] || die "после --lang нужен ru или en"
		LANG_CODE="$2"
		LANG_SET=1
		shift 2
		;;
	--lang=*)
		LANG_CODE="${1#*=}"
		LANG_SET=1
		shift
		;;
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
	--skip-upgrade)
		SKIP_UPGRADE=1
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
	--acme-dns-provider)
		[ "$#" -ge 2 ] || die "после --acme-dns-provider нужен провайдер"
		ACME_DNS_PROVIDER="$2"
		shift 2
		;;
	--acme-dns-provider=*)
		ACME_DNS_PROVIDER="${1#*=}"
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
	--acme-dns-token-file)
		[ "$#" -ge 2 ] || die "после --acme-dns-token-file нужен путь"
		ACME_DNS_TOKEN_FILE="$2"
		shift 2
		;;
	--acme-dns-token-file=*)
		ACME_DNS_TOKEN_FILE="${1#*=}"
		shift
		;;
	--acme-dns-zone-id)
		[ "$#" -ge 2 ] || die "после --acme-dns-zone-id нужен идентификатор"
		ACME_DNS_ZONE_ID="$2"
		shift 2
		;;
	--acme-dns-zone-id=*)
		ACME_DNS_ZONE_ID="${1#*=}"
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
	--panel-mode)
		[ "$#" -ge 2 ] || die "после --panel-mode нужен server или subdomain"
		PANEL_MODE="$2"
		shift 2
		;;
	--panel-mode=*)
		PANEL_MODE="${1#*=}"
		shift
		;;
	--panel-domain)
		[ "$#" -ge 2 ] || die "после --panel-domain нужен домен"
		PANEL_DOMAIN="$2"
		shift 2
		;;
	--panel-domain=*)
		PANEL_DOMAIN="${1#*=}"
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
		die "неизвестный аргумент: $1 (--help)"
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

# Язык — до первого информационного сообщения и до вопросов конфигуратора.
# --yes его не пропускает: это не подтверждение установки, а выбор языка.
ask_language

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
	*) die "$(translate 'Ubuntu не поддерживается: нужен LTS 20.04, 22.04, 24.04 или 26.04') ($OS_VERSION)" ;;
	esac
	;;
debian)
	case "$OS_VERSION" in
	12|12.*|13|13.*) ;;
	*) die "$(translate 'Debian не поддерживается: нужны Debian 12 или 13') ($OS_VERSION)" ;;
	esac
	;;
*)
	die "$(translate 'дистрибутив не поддерживается: нужны Ubuntu LTS или Debian 12/13') ($OS_NAME)"
	;;
esac

case "$(uname -m)" in
x86_64|amd64) ARCH="amd64" ;;
aarch64|arm64) ARCH="arm64" ;;
*) die "$(translate 'архитектура не поддерживается: нужны x86_64 или aarch64') ($(uname -m))" ;;
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
	if [ -n "$DOMAIN$ACME_EMAIL$ACME_DIRECTORY$ACME_DNS_PROVIDER$ACME_DNS_HOOK$ACME_DNS_TOKEN_FILE$ACME_DNS_ZONE_ID$PANEL_ACCESS$MASQUERADE$MASQUERADE_FILE$MASQUERADE_PROXY$PASSWORD_FILE" ] ||
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
	fi

	if [ "$INTERACTIVE" -eq 1 ]; then
		[ -n "$DOMAIN" ] || ask_domain
		[ -n "$ACME_EMAIL" ] || ask_email
		[ -n "$PANEL_ACCESS" ] || ask_access

		# Доступ спрашивается раньше панели: при public режим server
		# недоступен, и вопрос о нём не задаётся. Явный --panel-domain уже
		# означает subdomain и вопроса о режиме не требует.
		if [ -z "$PANEL_MODE" ] && [ -n "$PANEL_DOMAIN" ]; then
			PANEL_MODE="subdomain"
		fi
		[ -n "$PANEL_MODE" ] || ask_panel_mode
		if [ -z "$PANEL_DOMAIN" ] && [ "$PANEL_MODE" != "server" ]; then
			ask_panel_domain
		fi

		[ -n "$ACME_MODE" ] || ask_acme_mode

		if [ "$ACME_MODE" = "dns" ]; then
			[ -n "$ACME_DNS_PROVIDER" ] || ask_acme_dns_provider
			case "$ACME_DNS_PROVIDER" in
			hook) [ -n "$ACME_DNS_HOOK" ] || ask_acme_dns_hook ;;
			cloudflare)
				[ -n "$ACME_DNS_TOKEN_FILE" ] || ask_acme_dns_token_file
				[ -n "$ACME_DNS_ZONE_ID" ] || ask_acme_dns_zone_id
				;;
			esac
		fi

		[ -n "$MASQUERADE" ] || ask_masquerade

		if [ "$MASQUERADE" = "file" ] && [ -z "$MASQUERADE_FILE" ]; then
			ask_masquerade_file
		fi
		if [ "$MASQUERADE" = "proxy" ] && [ -z "$MASQUERADE_PROXY" ]; then
			ask_masquerade_proxy
		fi
	fi

	# Свой пароль — только явным файлом. Во всех остальных случаях пароль
	# генерируется: статичного или угадываемого пароля у панели нет.
	if [ -n "$PASSWORD_FILE" ]; then
		PASSWORD_MODE="file"
	else
		PASSWORD_MODE="generate"
	fi

	validate_answers
	show_answers
	show_dns

	if [ "$INTERACTIVE" -eq 1 ] && [ "$ASSUME_YES" -eq 0 ]; then
		printf '%s ' "$(translate 'Продолжить установку? [y/N]:')" >&2
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

# --- обновление системы -----------------------------------------------------

# Свежий VPS часто приходит с устаревшими пакетами. Обновляем его до установки:
# иначе часть зависимостей и сам systemd остаются в состоянии «как в образе».
# NEEDRESTART_MODE=l отключает интерактивные вопросы needrestart, которые иначе
# остановили бы установку без терминала.
APT_UPDATED=0
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=l

if [ "$SKIP_UPGRADE" -eq 0 ] && [ "$CONFIGURED" -eq 0 ]; then
	info "обновляю систему: apt update и apt upgrade"
	apt-get -o DPkg::Lock::Timeout=120 update -qq ||
		die "не удалось обновить список пакетов"
	APT_UPDATED=1

	# force-confdef/force-confold: обновление не должно останавливаться на
	# вопросе о конфигурационном файле. На свежем сервере таких файлов обычно
	# нет, но образ может прийти с уже изменёнными настройками.
	apt-get -o DPkg::Lock::Timeout=120 -y \
		-o Dpkg::Options::=--force-confdef \
		-o Dpkg::Options::=--force-confold \
		upgrade ||
		die "не удалось обновить систему"
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

	if [ "$APT_UPDATED" -eq 0 ]; then
		apt-get -o DPkg::Lock::Timeout=120 update -qq ||
			die "не удалось обновить список пакетов"
		APT_UPDATED=1
	fi

	# Список собран циклом выше; кавычки здесь разбивали бы его на аргументы.
	# shellcheck disable=SC2086
	apt-get -o DPkg::Lock::Timeout=120 install -y --no-install-recommends $MISSING ||
		die "не удалось поставить зависимости"
fi

# Без curl не скачать ни архив, ни контрольные суммы. На минимальном образе его
# может не быть; выше он уже поставлен из штатного репозитория. Проверка отдельная,
# чтобы ошибка говорила о причине, а не «не удалось скачать».
command -v curl >/dev/null 2>&1 ||
	die "не найден curl: установите его командой apt-get install -y curl"
command -v sha256sum >/dev/null 2>&1 ||
	die "не найден sha256sum: установите пакет coreutils"

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

# Токен Cloudflare читает команда setup от имени службы. Исходный файл может
# лежать в /root; временная копия делает его доступным только службе, а после
# записи в базу удаляется.
if [ "$CONFIGURED" -eq 0 ] && [ "$ACME_MODE" = "dns" ] && [ "$ACME_DNS_PROVIDER" = "cloudflare" ]; then
	ACME_TOKEN_TMP="$STATE_DIR/.cloudflare-token"
	install -m 0600 -o "$SERVICE_USER" -g "$SERVICE_GROUP" "$ACME_DNS_TOKEN_FILE" "$ACME_TOKEN_TMP" ||
		die "не удалось подготовить файл с токеном Cloudflare"
	ACME_DNS_TOKEN_FILE="$ACME_TOKEN_TMP"
fi

# Каталог сайта читает уже не root, а служба. Проверяем это от её имени: иначе
# установка дошла бы до записи базы и упала на первом же обращении к сайту.
if [ "$CONFIGURED" -eq 0 ] && [ "$MASQUERADE" = "file" ]; then
	if ! runuser -u "$SERVICE_USER" -- test -r "$MASQUERADE_FILE" ||
		! runuser -u "$SERVICE_USER" -- test -x "$MASQUERADE_FILE"; then
		die "$(translate 'каталог сайта недоступен пользователю') $SERVICE_USER: $MASQUERADE_FILE — $(translate 'нужны права на чтение и проход')"
	fi
fi

# Хук DNS запускает служба, а не root: проверяем его от её имени. Файл с
# токеном Cloudflare тоже читает команда настройки от имени службы.
if [ "$CONFIGURED" -eq 0 ] && [ "$ACME_MODE" = "dns" ]; then
	case "$ACME_DNS_PROVIDER" in
	cloudflare)
		if ! runuser -u "$SERVICE_USER" -- test -r "$ACME_DNS_TOKEN_FILE"; then
			die "$(translate 'файл с токеном Cloudflare недоступен пользователю') $SERVICE_USER: $ACME_DNS_TOKEN_FILE"
		fi
		;;
	*)
		if ! runuser -u "$SERVICE_USER" -- test -x "$ACME_DNS_HOOK"; then
			die "$(translate 'хук DNS недоступен пользователю') $SERVICE_USER: $ACME_DNS_HOOK — $(translate 'нужен исполняемый файл')"
		fi
		;;
	esac
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
		# Сводка setup не печатается: у установщика свой локализованный итог, а
		# смешивать русский вывод setup с английским установщиком нельзя.
		printf '%s\n' "$ADMIN_PASSWORD" | run_setup "$BIN" stdin service >/dev/null ||
			die "не удалось сохранить настройки"
		;;
	generate)
		# Пароль печатается не здесь, а в самом конце: в длинном выводе
		# обновления и запуска его легко не заметить. Сводку setup не печатаем
		# по той же причине, что и выше; из неё нужна только строка с паролем.
		SETUP_OUT="$(run_setup "$BIN" generate service)" ||
			die "не удалось сохранить настройки"
		ADMIN_PASSWORD="$(printf '%s\n' "$SETUP_OUT" | sed -n 's/^пароль администратора: //p')"
		[ -n "$ADMIN_PASSWORD" ] ||
			die "не удалось получить сгенерированный пароль администратора"
		;;
	*)
		die "не задан пароль администратора"
		;;
	esac
fi

# Токен уже в базе: временная копия больше не нужна.
if [ -n "$ACME_TOKEN_TMP" ]; then
	rm -f "$ACME_TOKEN_TMP"
	ACME_TOKEN_TMP=""
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
	print_generated_password
	exit 1
fi

# --- итог ------------------------------------------------------------------

if [ "$CONFIGURED" -eq 0 ]; then
	CERT_SUMMARY="$(translate 'Способ проверки:') ${ACME_TEXT}."
	case "$ACME_MODE" in
	dns)
		case "$ACME_DNS_PROVIDER" in
		cloudflare)
			ACME_NOTE="$(translate 'Для DNS-01 панель создаёт TXT-запись через Cloudflare. Токен уже в базе: файл с ним можно удалить.')"
			;;
		*)
			ACME_NOTE="$(translate 'Для DNS-01 панель вызывает указанный хук; он должен оставаться доступен службе.')"
			;;
		esac
		;;
	*)
		ACME_NOTE="$(translate 'Для HTTP-01 домен должен указывать на этот сервер, а порт 80 быть свободен.')"
		;;
	esac
else
	CERT_SUMMARY="$(translate 'Настройки ACME сохранены при установке.')"
	ACME_NOTE=""
fi

if [ "$CONFIGURED" -eq 1 ]; then
	PANEL_INFO="$(translate 'Панель уже настроена: адрес и сертификат не менялись.')"
elif [ "$PANEL_ACCESS" = "public" ]; then
	PANEL_INFO="$(translate 'Панель слушает 8443 на всех интерфейсах:')
  https://${PANEL_HOST}:8443
  $(translate 'после выпуска сертификата.')"
else
	if [ "$LANG_CODE" = "en" ]; then
		SERVER_ADDR="<server-address>"
	else
		SERVER_ADDR="<адрес-сервера>"
	fi
	PANEL_INFO="$(translate 'Панель спрятана на петле. Проброс:')
  ssh -L 8443:127.0.0.1:8443 root@${SERVER_ADDR}
  $(translate 'Открыть:') https://127.0.0.1:8443
  $(translate 'Имя сертификата:') ${PANEL_HOST}"
fi

info "готово"
"$BIN" --version

if [ "$LANG_CODE" = "en" ]; then
	cat <<EOF

Service:   systemctl status ${PRODUCT}
Database:  ${DB}
Log:       ${LOG}

${PANEL_INFO}

The panel issues and renews the certificate itself. ${CERT_SUMMARY}
${ACME_NOTE} Issuance runs in the background; the panel is already working.

Change the administrator password:
  systemctl stop ${PRODUCT}
  runuser -u ${PRODUCT} -- ${BIN} passwd
  systemctl start ${PRODUCT}
EOF
else
	cat <<EOF

Служба:   systemctl status ${PRODUCT}
База:     ${DB}
Журнал:   ${LOG}

${PANEL_INFO}

Сертификат панель выпускает и продлевает сама. ${CERT_SUMMARY}
${ACME_NOTE} Выпуск идёт в фоне, панель при этом уже работает.

Сменить пароль администратора:
  systemctl stop ${PRODUCT}
  runuser -u ${PRODUCT} -- ${BIN} passwd
  systemctl start ${PRODUCT}
EOF
fi

if [ "$USE_DEBUG" -eq 1 ]; then
	if [ "$LANG_CODE" = "en" ]; then
		cat <<EOF

There is no certificate for the domain yet, so the panel is running with a
self-signed one: the browser will warn about an untrusted certificate. This is
a temporary mode until ACME issues the certificate. The panel will issue it
itself and keep running; after a file appears in ${CERTS_DIR}, running the
installer again removes the temporary mode.
See the panel for the issuance error and retry.
EOF
	else
		cat <<EOF

Сертификата для домена пока нет, поэтому панель поднята с самоподписанным:
браузер предупредит о недоверенном сертификате. Это временный режим до выпуска
сертификата ACME. Панель выпустит его сама и продолжит работать; после появления
файла в ${CERTS_DIR} повторный запуск установщика уберёт временный режим.
Причину отказа выпуска и повтор смотрите в интерфейсе панели.
EOF
	fi
fi

# Пароль — последним блоком: он генерируется при установке и показывается ровно
# один раз. В базе лежит только хеш, восстановить его оттуда нельзя.
if [ "$CONFIGURED" -eq 0 ] && [ "$PASSWORD_MODE" = "generate" ]; then
	if [ "$LANG_CODE" = "en" ]; then
		cat <<EOF

============================================================
  Panel access
  name:     admin
  password: ${ADMIN_PASSWORD}
============================================================

The password is shown once. Save it: the database contains only a hash,
and the password cannot be recovered from there.
EOF
	else
		cat <<EOF

============================================================
  Доступ в панель
  имя:    admin
  пароль: ${ADMIN_PASSWORD}
============================================================

Пароль показан один раз. Сохраните его: в базе лежит только хеш,
и восстановить пароль оттуда нельзя.
EOF
	fi
elif [ "$CONFIGURED" -eq 1 ]; then
	if [ "$LANG_CODE" = "en" ]; then
		cat <<EOF

The administrator password was not changed: running the installer again does
not touch it.
EOF
	else
		cat <<EOF

Пароль администратора не менялся: повторный запуск установщика его не трогает.
EOF
	fi
fi
