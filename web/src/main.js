import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'
import { getLocale } from './paraglide/runtime.js'

// Язык документа нужен браузеру и экранным дикторам: без него английская
// страница читалась бы по правилам русского произношения.
document.documentElement.lang = getLocale()

// Интерфейс — одностраничное приложение: единственная точка монтирования на всю
// панель, дальше страницами заведует клиент.
mount(App, { target: document.getElementById('app') })
