// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightSiteGraph from 'starlight-site-graph'
import { fileURLToPath } from 'node:url';

// https://astro.build/config
export default defineConfig({
	site: 'https://cgs-earth.github.io',
	base: '/sal',
	vite: {
		resolve: {
			alias: {
				micromatch: fileURLToPath(new URL('./src/shims/micromatch.js', import.meta.url)),
			},
		},
		optimizeDeps: {
			exclude: ['micromatch'],
		},
	},
	integrations: [
		starlight({
			plugins: [starlightSiteGraph()],
			title: 'SAL Docs',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/cgs-earth/sal' }],
			sidebar: [
				{
					label: 'Guides',
					items: [{ autogenerate: { directory: 'guides' } }],
				},
				{
					label: 'Reference',
					items: [{ autogenerate: { directory: 'reference' } }],
				},
			],
		}),
	],
});
