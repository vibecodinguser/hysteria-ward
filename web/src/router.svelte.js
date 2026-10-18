// Маршрутизация на клиенте своя: серверной части у интерфейса нет, а заводить
// ради разбора адреса целый фреймворк со сборкой статики незачем.
//
// Сервер на любой незнакомый путь отдаёт точку входа приложения, поэтому адрес
// разбирается здесь и только здесь.

let path = $state(window.location.pathname)

/** Текущий адрес панели. */
export function currentPath() {
	return path
}

/** Перейти по адресу, не перезагружая страницу. */
export function navigate(to) {
	if (to === path) {
		return
	}

	history.pushState(null, '', to)
	path = to
}

/**
 * Слушать переходы: кнопки «назад» и «вперёд» браузера и обычные ссылки.
 * Вызывается один раз за жизнь страницы.
 */
export function followHistory() {
	window.addEventListener('popstate', () => {
		path = window.location.pathname
	})

	document.addEventListener('click', (event) => {
		// Чужие обработчики, новая вкладка и клавиши-модификаторы — не наше дело:
		// такие переходы обязан обработать браузер.
		if (event.defaultPrevented || event.button !== 0) {
			return
		}
		if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
			return
		}

		const link = event.target.closest?.('a[href]')
		if (!link || link.target || link.origin !== window.location.origin) {
			return
		}

		event.preventDefault()
		navigate(link.pathname)
	})
}
