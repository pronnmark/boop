import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'
import { registerWorker } from './lib/push'

const app = mount(App, { target: document.getElementById('app')! })

// Register the push service worker up front so events arrive without the app
// being opened first.
registerWorker()

export default app
