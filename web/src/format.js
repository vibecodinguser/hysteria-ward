// Форматирование чисел для интерфейса: байты, скорость и время.
//
// Считаем в двоичных единицах (1024): так объём показывают и браузеры, и
// роутеры, а «мегабайт» в панели должен означать то же, что в клиенте.

const units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ', 'ПБ']

/**
 * Объём: «12,3 МБ». До килобайта — целые байты, дальше две значащие цифры после
 * единиц, чтобы столбец не дёргался на каждой сотой.
 */
export function formatBytes(bytes) {
	let value = Number(bytes) || 0
	let unit = 0

	while (value >= 1024 && unit < units.length - 1) {
		value /= 1024
		unit += 1
	}

	const digits = unit === 0 ? 0 : value >= 100 ? 0 : value >= 10 ? 1 : 2
	const number = new Intl.NumberFormat('ru-RU', { maximumFractionDigits: digits }).format(value)

	return number + ' ' + units[unit]
}

/** Скорость: «12,3 МБ/с». */
export function formatRate(bytesPerSecond) {
	return formatBytes(bytesPerSecond) + '/с'
}

/** Байты в секунду в Мбит/с: так скорость задают в настройках. */
export function toMbps(bytesPerSecond) {
	const value = (Number(bytesPerSecond) || 0) * 8 / 1e6

	return Math.round(value * 100) / 100
}

/** Мбит/с в байты в секунду: ядро считает в байтах. */
export function fromMbps(mbps) {
	const value = Number(mbps)
	if (!Number.isFinite(value) || value <= 0) {
		return 0
	}

	return Math.round(value * 1e6 / 8)
}

/**
 * Время окна: часы и минуты, а для суток и недели ещё и дата — иначе на семи
 * сутках подписи были бы неразличимы.
 */
export function formatClock(at, withDate = false) {
	const date = new Date(at)

	const format = withDate
		? { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' }
		: { hour: '2-digit', minute: '2-digit' }

	return new Intl.DateTimeFormat('ru-RU', format).format(date)
}
