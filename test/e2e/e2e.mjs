// Browser steps of the end-to-end test, driven by run.sh.
import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = 'http://localhost:8000';
const step = process.argv[2];
let failures = 0;

function check(name, ok, detail = '') {
    console.log(`${ok ? 'PASS' : 'FAIL'} [${step}] ${name}${!ok && detail ? ' :: ' + detail : ''}`);
    if (!ok) failures++;
}

const browser = await chromium.launch();
const consoleErrors = [];
let lastPage = null;

async function session(user) {
    const file = `/work/${user}.json`;
    const ctx = await browser.newContext({ storageState: fs.existsSync(file) ? file : undefined });
    const page = await ctx.newPage();
    // error pages answer with 4xx, the browser logs that as a failed resource
    page.on('console', (m) => { if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) consoleErrors.push(m.text()); });
    page.on('pageerror', (e) => consoleErrors.push(e.message));
    lastPage = page;
    return { ctx, page, save: () => ctx.storageState({ path: file }) };
}

async function login(page, user, groups) {
    await page.goto(BASE + '/');
    if (await page.locator('#sheet-account').count()) return;
    await page.getByRole('link', { name: 'Sign in with Cloudron' }).click();
    await page.fill('input[name=username]', user);
    await page.fill('textarea[name=claims]', JSON.stringify({ groups, name: user }));
    await page.click('input[type=submit]');
    await page.waitForURL(BASE + '/');
}

const text = (page) => page.locator('main').innerText();

// 1x1 PNG
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');

const steps = {
    async admin() {
        const { page, save } = await session('ann');
        await login(page, 'ann', ['bot-admins']);
        await save();
        let t = await text(page);
        check('dashboard renders', t.includes('Dashboard') && t.includes('Next 24 hours'));
        check('offline notice', t.includes('The bot is not connected to Discord') && t.includes('DISCORD_TOKEN is not set'), t.slice(0, 300));
        check('admin link for admin group', await page.locator('.nav a[href="/admin"]').count() === 1);

        await page.goto(BASE + '/admin');
        check('admin page shows group', (await text(page)).includes('bot-admins'));
        await page.goto(BASE + '/admin/settings');
        await page.fill('input[name=max_upload_mb]', '50');
        await page.click('main form button[type=submit]');
        check('settings saved', (await text(page)).includes('Saved.') && await page.inputValue('input[name=max_upload_mb]') === '50');

        await page.goto(BASE + '/media');
        t = await text(page);
        check('imported sound listed', t.includes('beep.ogg') && /0:\d\d/.test(t), t.slice(0, 500));
        check('upload limit from settings', t.includes('up to 50 MB'));
        await page.setInputFiles('input[type=file]', { name: 'friday card.png', mimeType: 'image/png', buffer: png });
        await page.click('form[data-upload] button[type=submit]');
        t = await text(page);
        check('image uploaded', t.includes('Uploaded.') && t.includes('friday_card.png') && t.includes('1 × 1'), t.slice(0, 500));
        const img = await page.request.get(BASE + (await page.locator('img[src^="/media/"]').first().getAttribute('src')));
        check('image served inline', img.ok() && img.headers()['content-type'] === 'image/png');
        const audio = await page.request.get(BASE + (await page.locator('audio').first().getAttribute('src')), { headers: { Range: 'bytes=0-3' } });
        check('sound supports range requests', audio.status() === 206 && (await audio.body()).toString() === 'OggS');

        await page.goto(BASE + '/messages/new');
        await page.fill('input[name=name]', 'Friday reminder');
        await page.fill('textarea[name=content]', 'Jumu’ah Mubarak {date}');
        await page.click('button[value=add-embed]');
        check('editor op keeps unsaved state', (await text(page)).includes('Changes are not saved yet') && await page.inputValue('input[name=name]') === 'Friday reminder');
        await page.fill('input[name="e0.title"]', 'Friday prayer');
        check('live preview', (await page.locator('[data-preview-target="title-0"]').innerText()) === 'Friday prayer');
        check('mention picker shown with JS', await page.locator('input[data-insert=content]').isVisible());
        await page.route('**/members?*', (route) => route.fulfill({ json: [{ id: '111111111111111111', label: 'Jane (@jane.doe)', name: 'Jane', username: 'jane.doe' }] }));
        await page.locator('textarea[name=content]').evaluate((a) => a.setSelectionRange(a.value.length, a.value.length));
        await page.locator('input[data-insert=content]').pressSequentially('ja');
        await page.locator('.suggestions li').first().click();
        check('mention inserted into the text', (await page.inputValue('textarea[name=content]')).endsWith('<@111111111111111111>'));
        await page.unroute('**/members?*');
        await page.selectOption('select[name="e0.image"]', { label: 'friday_card.png' });
        await page.click('button[value="add-field:0"]');
        await page.fill('input[name="e0.f0.name"]', 'Khutbah');
        await page.fill('input[name="e0.f0.value"]', '13:15');
        await page.click('main .btns button[value=save]');
        t = await text(page);
        check('message saved', t.includes('Saved.') && t.includes('Khutbah'), t.slice(0, 400));
        check('embed image in preview', await page.locator('.dembed img.dimg').count() === 1);
        check('placeholder expanded', /Mubarak \d{4}-\d\d-\d\d/.test(await page.locator('[data-preview-target=content]').innerText()));

        await page.goto(BASE + '/media');
        check('media shows usage', (await text(page)).includes('used by message Friday reminder'));
        await page.locator('[popovertarget^="sheet-edit-"]').first().click();
        const editSheet = page.locator('[id^="sheet-edit-"]:popover-open');
        await editSheet.locator('input[name=tags]').fill('Greeting, funny');
        await editSheet.locator('button[type=submit]').click();
        t = await text(page);
        check('tags saved', t.includes('Saved.') && await page.locator('.list a.chip', { hasText: 'greeting' }).count() === 1);
        await page.click('.pills a:has-text("greeting")');
        check('tag filter', (await page.locator('main .list > li').count()) === 1);
        await page.goto(BASE + '/media');
        await page.fill('input[name=url]', 'http://127.0.0.1:8000/healthz');
        await page.click('form[action="/media/url"] button[type=submit]');
        check('link to the app itself refused', (await text(page)).includes('is not a public address'), (await text(page)).slice(0, 300));

        await page.goto(BASE + '/tasks/new');
        await page.fill('input[name=name]', 'Adhan');
        await page.check('input[name=kind][value=daily]', { force: true });
        await page.fill('input[name=times]', '18:15, 5:00');
        await page.click('button[value="add:play"]');
        await page.click('button[value="add:wait"]');
        await page.click('button[value="add:leave"]');
        await page.click('button[value="up:2"]');
        const types = await page.locator('input[name$=".type"]').evaluateAll((els) => els.map((e) => e.value));
        check('steps added and moved', types.join() === 'play,leave,wait', types.join());
        await page.click('button[value="add:dm"]');
        await page.fill('input[name="s3.user"]', 'nobody');
        await page.click('main .btns button[value=save]');
        t = await text(page);
        check('task needs a server', t.includes('Not saved.') && t.includes('Pick a server'), t.slice(0, 500));
        check('unknown member reported', t.includes('no member of this server matches "nobody"') && await page.inputValue('input[name="s3.user"]') === 'nobody');
        await page.route('**/members?*', (route) => route.fulfill({ json: [
            { id: '111111111111111111', label: 'Jane (@jane.doe)', name: 'Jane', username: 'jane.doe' },
            { id: '222222222222222222', label: 'Amira (@amira)', name: 'Amira', username: 'amira' }] }));
        await page.fill('input[name="s3.user"]', '');
        await page.locator('input[name="s3.user"]').pressSequentially('ja');
        await page.locator('.suggestions li').first().waitFor();
        await page.keyboard.press('ArrowDown');
        await page.keyboard.press('Enter');
        check('picker fills the chosen member', await page.inputValue('input[name="s3.user"]') === 'Amira (@amira)');
        check('enter in picker does not submit', page.url().endsWith('/tasks/new') && (await text(page)).includes('Not saved.'));
        await page.unroute('**/members?*');
        const members = await page.request.get(BASE + '/members?q=a');
        check('member search answers JSON', members.ok() && Array.isArray(await members.json()));
        check('times normalized', await page.inputValue('input[name=times]') === '05:00 18:15');
        check('next runs preview', (await page.locator('aside .list .when').count()) === 5);

        const res = await page.request.post(BASE + '/tasks/new', { form: { op: 'save', name: 'x' } });
        check('post without csrf token rejected', res.status() === 403);

        await page.goto(BASE + '/admin/audit');
        t = await text(page);
        check('audit log', ['media.upload', 'message.create', 'settings.update'].every((a) => t.includes(a)), t.slice(0, 500));
        await save();
    },

    async user() {
        const { page, save } = await session('bob');
        await login(page, 'bob', []);
        check('no admin link', await page.locator('.nav a[href="/admin"]').count() === 0);
        await page.goto(BASE + '/admin');
        check('admin page refused', (await text(page)).includes('Only admins'));
        await page.goto(BASE + '/messages');
        check('user sees shared messages', (await text(page)).includes('Friday reminder'));
        await save();
    },

    // long names without spaces, like a server called openpgp4fpr:3EFABCB7..., must not widen the page
    async layout() {
        const { page } = await session('ann');
        const long = 'openpgp4fpr_3EFABCB708412AEE165EDC6C7B17D5A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F6';
        await page.goto(BASE + '/media');
        await page.setInputFiles('input[type=file]', { name: long + '.png', mimeType: 'image/png', buffer: png });
        await page.click('form[data-upload] button[type=submit]');
        await page.goto(BASE + '/messages/new');
        await page.fill('input[name=name]', long);
        await page.fill('textarea[name=content]', long + long);
        await page.click('main .btns button[value=save]');
        const msg = page.url();
        await page.goto(BASE + '/tasks/new');
        await page.fill('input[name=name]', long);
        await page.click('button[value="add:message"]');
        const task = page.url();
        for (const width of [1280, 390]) {
            await page.setViewportSize({ width, height: 900 });
            for (const path of ['/media', '/messages', msg.replace(BASE, ''), '/admin/settings', '/admin/audit']) {
                await page.goto(BASE + path);
                const wide = await page.evaluate(() => [...document.querySelectorAll('main *')]
                    .filter((el) => {
                        if (el.offsetParent === null || el.closest('.url, .tabs, [popover]')) return false;
                        const box = el.parentElement.closest('.card, .danger-box, .embed-ed, .step, .dpv, main');
                        return el.getBoundingClientRect().right > Math.min(box.getBoundingClientRect().right, window.innerWidth) + 1;
                    })
                    .map((el) => el.tagName.toLowerCase() + '.' + el.className).slice(0, 5));
                check(`no overflow on ${path.split('?')[0]} at ${width} px`, wide.length === 0, wide.join(', '));
            }
        }
        check('task editor reached', task.includes('/tasks/new'));
    },

    async promoted() {
        const { page } = await session('bob');
        await page.goto(BASE + '/admin');
        check('same session is admin now', (await text(page)).includes('BOT_ADMINS') && (await text(page)).includes('carol.smith'));
    },

    // store and README screenshots, run with SCREENSHOTS=1
    async shots() {
        const { ctx, page } = await session('ann');
        const shot = async (path, name, opts = {}) => {
            await page.goto(BASE + path);
            await page.evaluate(() => document.fonts.ready);
            await page.screenshot({ path: `/work/shots/${name}.png`, fullPage: true, ...opts });
        };
        fs.mkdirSync('/work/shots', { recursive: true });
        await page.setViewportSize({ width: 1280, height: 900 });
        await shot('/', 'dashboard');
        await shot('/media', 'media');
        await shot('/messages/1', 'message');
        await page.goto(BASE + '/tasks/new');
        await page.fill('input[name=name]', 'Adhan');
        await page.fill('input[name=times]', '05:00 11:50 14:00 16:35 18:15');
        for (const t of ['join', 'play', 'message', 'wait', 'leave']) await page.click(`button[value="add:${t}"]`);
        await page.screenshot({ path: '/work/shots/task.png', fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        await shot('/', 'dashboard-phone', { fullPage: false });
        await shot('/messages/1', 'message-phone');
        await ctx.close();
    },

    async persisted() {
        const { page } = await session('ann');
        await page.goto(BASE + '/media');
        const t = await text(page);
        check('session survives restart', await page.locator('#sheet-account').count() === 1);
        check('media survive restart', t.includes('friday_card.png') && t.includes('beep.ogg'));
        await page.goto(BASE + '/tasks');
        await page.getByRole('link', { name: 'Every minute', exact: true }).click();
        check('missed runs in the history', (await page.locator('[data-live=runs]').innerText()).includes('Missed, the app was not running.'));
        await page.goto(BASE + '/tasks');
        await page.getByRole('link', { name: 'Greeting', exact: true }).click();
        check('event task in the editor', await page.locator('input[name=kind][value=event]').isChecked() && await page.inputValue('input[name=cooldown]') === '45'
            && await page.locator('select[name="s0.to"]').inputValue() === 'trigger' && await page.locator('.picker.dm-member').isHidden());
        check('event task summary', (await page.locator('main').innerText()).includes('Runs when the event happens.'));
        const toks = () => page.locator('.tokens .tok input[type=hidden]').evaluateAll((els) => els.map((e) => e.value).join());
        const count = () => page.locator('[data-count]').innerText();
        check('member tokens rendered', await toks() === '333333333333333333' && await count() === '1 of 25');
        await page.route('**/members?*', (route) => route.fulfill({ json: [
            { id: '111111111111111111', label: 'Jane (@jane.doe)', name: 'Jane', username: 'jane.doe' },
            { id: '333333333333333333', label: 'Old (@old)', name: 'Old', username: 'old' }] }));
        const search = page.locator('.tokens input[type=search]');
        await search.pressSequentially('j');
        await page.locator('.tokens .suggestions li').first().waitFor();
        check('picked members not suggested', await page.locator('.tokens .suggestions li').count() === 1);
        await page.keyboard.press('Enter');
        check('enter adds a token', await toks() === '333333333333333333,111111111111111111' && await count() === '2 of 25' && await search.inputValue() === '');
        await page.keyboard.press('Backspace');
        check('backspace removes the last token', await toks() === '333333333333333333');
        await search.pressSequentially('j');
        await page.locator('.tokens .suggestions li').first().waitFor();
        await page.keyboard.press('Enter');
        await page.locator('.tok button[aria-label="Remove 333333333333333333"]').click();
        check('x removes a token', await toks() === '111111111111111111' && page.url().includes('/tasks/'));
        await page.unroute('**/members?*');
        await page.click('main .btns button[value=save]');
        await page.goto(BASE + '/tasks');
        await page.getByRole('link', { name: 'Greeting', exact: true }).click();
        check('member tokens saved', await toks() === '111111111111111111', await toks());
    },
};

try {
    await steps[step]();
} catch (e) {
    check('step finished', false, e.message.split('\n')[0]);
}
const csp = consoleErrors.filter((e) => /Content Security Policy|Refused/.test(e));
check('no CSP violations', csp.length === 0, csp.join(' | '));
check('no script errors', consoleErrors.length === 0, consoleErrors.join(' | '));
if (failures > 0 && lastPage) {
    fs.writeFileSync(`/work/fail-${step}.html`, await lastPage.content());
    console.log(`       page saved to fail-${step}.html (${lastPage.url()})`);
}
await browser.close();
process.exit(failures > 0 ? 1 : 0);
