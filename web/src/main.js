import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'

// Интерфейс — одностраничное приложение: единственная точка монтирования на всю
// панель, дальше страницами заведует клиент.
mount(App, { target: document.getElementById('app') })
