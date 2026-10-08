// Usage: node record.mjs <outdir>   (server on 127.0.0.1:8080)
import { chromium, firefox, webkit } from 'playwright';

const out = process.argv[2];
const goods = ['good-single-entity', 'good-collection', 'good-pagination', 'good-hateoas-collection',
  'good-hateoas-order-pending', 'good-hateoas-order-paid', 'good-deprecated', 'good-problem-403',
  'good-problem-422-validation'];
const g = (n) => `GET:${n}`;
const browsers = { chromium, firefox, webkit };
const scenarios = [
  ['good', Object.keys(browsers), [...goods.map(g), 'DELETE:good-delete-204']],
  ['bad-naming-camel-case', ['chromium'], [g('good-single-entity'), g('bad-naming-camel-case-snake'),
    g('bad-naming-camel-case-snake'), g('bad-naming-camel-case-snake'), g('good-collection')]],
  ['bad-problem-details', ['chromium'], [g('good-single-entity'), g('bad-problem-details-missing-title'), g('good-problem-403')]],
  ['bad-hypermedia-shape', ['chromium'], [g('good-single-entity'), g('bad-hypermedia-shape-action-get'), g('good-hateoas-order-pending')]],
  ['bad-delete-status', ['chromium'], [g('good-single-entity'), 'DELETE:bad-delete-status-delete-201', g('good-collection')]],
];

for (const [name, bs, calls] of scenarios) {
  for (const b of bs) {
    const browser = await browsers[b].launch();
    const ctx = await browser.newContext({ recordHar: { path: `${out}/${name}-${b}.har`, content: 'embed' } });
    const page = await ctx.newPage();
    await page.goto(`http://127.0.0.1:8080/?calls=${calls.join(',')}`);
    await page.waitForFunction(() => document.title === 'done', null, { timeout: 30000 });
    await ctx.close();
    await browser.close();
    console.log('recorded', name, b);
  }
}
