import { mount } from 'svelte';
import App from './App.svelte';
import { applyPreferences, readPreferences } from './lib/themes/preferences';
import './app.css';

applyPreferences(readPreferences());

const target = document.getElementById('app');
if (!target) throw new Error('Application mount element is missing');
mount(App, { target });
